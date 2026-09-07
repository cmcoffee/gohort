package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEventMonitorStoreRoundTrip(t *testing.T) {
	db := memDB(t)
	m := EventMonitor{
		Name: "ts-join", Owner: "craig", Kind: EventKindWebhook,
		WakeBrief: "someone joined", Token: NewEventToken(),
	}
	SaveEventMonitor(db, m)

	got, ok := GetEventMonitor(db, "craig", "ts-join")
	if !ok || got.Token != m.Token || got.Kind != EventKindWebhook {
		t.Fatalf("GetEventMonitor round-trip failed: %+v ok=%v", got, ok)
	}
	// Token lookup (the public-endpoint path) finds it.
	byTok, ok := FindEventMonitorByToken(db, m.Token)
	if !ok || byTok.Name != "ts-join" {
		t.Fatalf("FindEventMonitorByToken failed: %+v ok=%v", byTok, ok)
	}
	if _, ok := FindEventMonitorByToken(db, "nope"); ok {
		t.Fatalf("FindEventMonitorByToken matched a bogus token")
	}
	// Owner scoping: another owner doesn't see it.
	if _, ok := GetEventMonitor(db, "other", "ts-join"); ok {
		t.Fatalf("monitor leaked across owners")
	}
	DeleteEventMonitor(db, "craig", "ts-join")
	if _, ok := GetEventMonitor(db, "craig", "ts-join"); ok {
		t.Fatalf("DeleteEventMonitor left the record")
	}
}

func TestEventMatch(t *testing.T) {
	cases := []struct {
		answer, match string
		want          bool
	}{
		{"YES, two new CVEs", "", true},         // default match = YES
		{"no change", "", false},                // default match = YES, absent
		{"status: ALERT raised", "alert", true}, // case-insensitive custom
		{"all quiet", "ALERT", false},           // custom absent
		{"", "", false},                         // empty answer never fires
	}
	for _, c := range cases {
		if got := eventMatch(c.answer, c.match); got != c.want {
			t.Errorf("eventMatch(%q, %q) = %v, want %v", c.answer, c.match, got, c.want)
		}
	}
}

func TestExtractJSONPath(t *testing.T) {
	body := []byte(`{"quoteResponse":{"result":[{"regularMarketPrice":142.37,"symbol":"NVDA"}]}}`)
	got, err := extractJSONPath(body, "quoteResponse.result.0.regularMarketPrice")
	if err != nil || got != "142.37" {
		t.Fatalf("extractJSONPath = %q, %v; want \"142.37\"", got, err)
	}
	if _, err := extractJSONPath(body, "quoteResponse.result.5.x"); err == nil {
		t.Fatalf("expected out-of-range index error")
	}
	if _, err := extractJSONPath([]byte("not json"), "a.b"); err == nil {
		t.Fatalf("expected non-JSON error")
	}
}

func TestExtractRegex(t *testing.T) {
	got, err := extractRegex([]byte(`price: $142.37 USD`), `\$([0-9.]+)`)
	if err != nil || got != "142.37" {
		t.Fatalf("extractRegex = %q, %v; want \"142.37\"", got, err)
	}
	if _, err := extractRegex([]byte("nope"), `\$([0-9.]+)`); err == nil {
		t.Fatalf("expected no-match error")
	}
}

func TestCompareValues(t *testing.T) {
	cases := []struct {
		ev, op, tv string
		want       bool
		wantErr    bool
	}{
		{"142.37", "<", "150", true, false},
		{"152", "<", "150", false, false},
		{"152", ">", "150", true, false},
		{"150", ">=", "150", true, false},
		{"ok", "==", "ok", true, false},
		{"up", "!=", "down", true, false},
		{"server is DOWN", "contains", "DOWN", true, false},
		{"abc", "<", "150", false, true}, // non-numeric
		{"1", "??", "2", false, true},    // bad op
	}
	for _, c := range cases {
		got, err := compareValues(c.ev, c.op, c.tv)
		if c.wantErr {
			if err == nil {
				t.Errorf("compareValues(%q,%q,%q) expected error", c.ev, c.op, c.tv)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("compareValues(%q,%q,%q) = %v,%v; want %v", c.ev, c.op, c.tv, got, err, c.want)
		}
	}
}

func TestExecuteEventPollFiresAndDebounces(t *testing.T) {
	db := memDB(t)
	m := EventMonitor{
		Name: "cve-watch", Owner: "craig", Kind: EventKindPoll,
		CheckAgent: "Security", Check: "any new CVEs?", IntervalSeconds: 60,
	}
	SaveEventMonitor(db, m)

	pollAnswer := "YES — CVE-2026-1"
	RegisterEventPoller(func(ctx context.Context, owner, agentID, check string) (string, error) {
		return pollAnswer, nil
	})
	defer RegisterEventPoller(nil)

	var wakes []string
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		wakes = append(wakes, summary)
		return true, ""
	})
	defer RegisterEventWaker(nil)

	// First match → wakes.
	executeEventPoll(context.Background(), db, m)
	if len(wakes) != 1 {
		t.Fatalf("expected 1 wake on first match, got %d", len(wakes))
	}
	// Same answer again → debounced (no new wake). Re-read so LastResult is set.
	cur, _ := GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected debounce on identical answer, got %d wakes", len(wakes))
	}
	// Changed but still-matching answer → edge-triggered, so NO new wake. It
	// re-fires only after the condition clears and re-arms.
	pollAnswer = "YES — CVE-2026-1, CVE-2026-2"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected no re-wake while still matching (edge-triggered), got %d", len(wakes))
	}
	// Non-matching answer → re-arms (no wake).
	pollAnswer = "no new CVEs"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected no wake on non-match, got %d", len(wakes))
	}
	// Matching again after re-arm → a fresh onset wakes.
	pollAnswer = "YES — CVE-2026-3"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 2 {
		t.Fatalf("expected a wake on the next onset after re-arm, got %d", len(wakes))
	}
}

// isSkipSentinel: only an EXPLICIT skip token suppresses now. Empty (the old
// silent-suppress default) must NOT be treated as skip — it fails open to the
// built-in summary instead, so a broken script can't quietly eat a change.
func TestIsSkipSentinel(t *testing.T) {
	skip := []string{"SKIP", "skip", " Skip \n", `{"skip":true}`, `{"skip": true}`}
	for _, s := range skip {
		if !isSkipSentinel(s) {
			t.Errorf("isSkipSentinel(%q) = false, want true", s)
		}
	}
	deliver := []string{"", "   ", "\n", "SouthPawn joined", "skipper", `{"skip":false}`, `{"other":true}`, "no", "0"}
	for _, s := range deliver {
		if isSkipSentinel(s) {
			t.Errorf("isSkipSentinel(%q) = true, want false (must deliver, not suppress)", s)
		}
	}
}

// A wake that reached nobody must not file a run saying it woke somebody.
// fireWake used to set RunOK the moment the waker returned, so a monitor
// deleted mid-flight, or one with no wake agent, recorded "Woke the Operator"
// — a delivery that did not happen. Silence would at least prompt a question.
func TestAnUndeliveredWakeIsNotRecordedAsSuccess(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return false, "the monitor no longer exists, so the event had nowhere to go"
	})
	defer RegisterEventWaker(nil)

	FireEventMonitor(context.Background(), db, EventMonitor{
		Name: "cve-watch", Owner: "craig", Kind: EventKindPoll,
	}, "CVE-2026-1 published")

	runs := ListRuns(db, "craig", RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
	if runs[0].Status != RunAttention {
		t.Errorf("an undelivered wake is attention, got %q", runs[0].Status)
	}
	if strings.Contains(runs[0].Summary, "Woke the Operator") {
		t.Errorf("it did not wake anyone: %q", runs[0].Summary)
	}
	if !strings.Contains(runs[0].Summary, "no longer exists") {
		t.Errorf("the row must carry WHY it was not delivered: %q", runs[0].Summary)
	}
}

// The delivering case keeps saying so, with the event in the summary.
func TestADeliveredWakeStillReadsAsSuccess(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return true, ""
	})
	defer RegisterEventWaker(nil)

	FireEventMonitor(context.Background(), db, EventMonitor{
		Name: "cve-watch", Owner: "craig", Kind: EventKindPoll,
	}, "CVE-2026-1 published")

	runs := ListRuns(db, "craig", RunFilter{})
	if len(runs) != 1 || runs[0].Status != RunOK {
		t.Fatalf("expected one ok run, got %+v", runs)
	}
	if !strings.Contains(runs[0].Summary, "Woke the Operator") {
		t.Errorf("summary lost the delivery: %q", runs[0].Summary)
	}
}

// TestRearmStrandedEventMonitors mirrors the standing-agent case: a stranded
// active scheduled monitor is rescheduled to a future check; paused, healthy,
// and non-scheduled (webhook) monitors are left alone.
func TestRearmStrandedEventMonitors(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	now := time.Now()

	// Stranded: active poll monitor, NextCheck long past, no live task.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "disk-watch", Kind: EventKindPoll, IntervalSeconds: 300,
		NextCheck: now.Add(-3 * time.Hour), SchedulerID: "dead-task",
	})
	// Paused: never re-arm.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "paused-watch", Kind: EventKindPoll, IntervalSeconds: 300,
		NextCheck: now.Add(-3 * time.Hour), Paused: true,
	})
	// Healthy: NextCheck in the future.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "healthy-watch", Kind: EventKindHTTP, IntervalSeconds: 600,
		NextCheck: now.Add(10 * time.Minute),
	})
	// Webhook: not a scheduled kind — nothing to re-arm.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "hook", Kind: EventKindWebhook,
	})

	revived := RearmStrandedEventMonitors(db)
	if revived != 1 {
		t.Fatalf("expected exactly 1 revived, got %d", revived)
	}

	m, _ := GetEventMonitor(db, "u", "disk-watch")
	if !m.NextCheck.After(now) {
		t.Fatalf("stranded monitor NextCheck not advanced to the future: %s", m.NextCheck)
	}
	if m.SchedulerID == "dead-task" || m.SchedulerID == "" {
		t.Fatalf("stranded monitor should have a fresh scheduler id, got %q", m.SchedulerID)
	}
	if p, _ := GetEventMonitor(db, "u", "paused-watch"); p.SchedulerID != "" {
		t.Error("paused monitor must not be re-armed")
	}

	if again := RearmStrandedEventMonitors(db); again != 0 {
		t.Fatalf("second sweep should revive nothing, got %d", again)
	}
}

func TestWatchPollFailed(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		want bool
	}{
		{"clean output", "SouthPawn has left TeamSpeak.", nil, false},
		{"tool error", "", errors.New("boom"), true},
		{"nonzero exit", "partial\n[exit: exit status 1]", nil, true},
		{"timeout", "[TIMED OUT after 1m30s — command killed.]", nil, true},
		{"python traceback", "Traceback (most recent call last):\n  File x\ngohort.HookError: fetch refused", nil, true},
	}
	for _, c := range cases {
		if got, _ := watchPollFailed(c.body, c.err); got != c.want {
			t.Errorf("%s: watchPollFailed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWatchFailureCircuitBreaker: K consecutive failed polls mark the monitor
// broken + paused (no delivery), and a success before K resets the streak.
func TestWatchFailureCircuitBreaker(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const traceback = "Traceback (most recent call last):\ngohort.HookError: binding revoked\n[exit: exit status 1]"

	result := traceback
	RegisterWatchToolInvoker(func(owner, agentID, toolName string, args map[string]any) (string, error) {
		return result, nil
	})
	defer RegisterWatchToolInvoker(nil)

	m := EventMonitor{Owner: "u", Name: "w", Kind: EventKindWatch, ToolName: "ts3_status", Notify: EventNotifyDirect}
	SaveEventMonitor(db, m)

	// Two failures — not yet broken.
	executeWatchPoll(context.Background(), db, m)
	executeWatchPoll(context.Background(), db, m)
	if got, _ := GetEventMonitor(db, "u", "w"); got.Broken {
		t.Fatalf("must not be broken before the threshold (failures=%d)", got.ConsecutiveFailures)
	}

	// A success resets the streak.
	result = "SouthPawn has left TeamSpeak."
	executeWatchPoll(context.Background(), db, m)
	if got, _ := GetEventMonitor(db, "u", "w"); got.ConsecutiveFailures != 0 || got.Broken {
		t.Fatalf("a successful poll must reset the streak; failures=%d broken=%v", got.ConsecutiveFailures, got.Broken)
	}

	// Now K straight failures → broken + paused.
	result = traceback
	for i := 0; i < monitorFailureThreshold; i++ {
		executeWatchPoll(context.Background(), db, m)
	}
	got, _ := GetEventMonitor(db, "u", "w")
	if !got.Broken || !got.Paused {
		t.Fatalf("after %d consecutive failures the monitor must be broken+paused; broken=%v paused=%v failures=%d",
			monitorFailureThreshold, got.Broken, got.Paused, got.ConsecutiveFailures)
	}
}

const watchCommentsA = `HTTP 200 OK
{"count":1,"has_more":false,"comments":[{"id":"4272e6b5","author":{"name":"gohort_agent","karma":709,"lastActive":"2026-09-05T05:34:54.685Z","createdAt":"2026-07-15T16:49:27.914Z"},"content":"first","created_at":"2026-09-02T10:00:00Z","parent_id":null}]}`

// The production case: the only difference is the author's lastActive.
const watchCommentsAgain = `HTTP 200 OK
{"count":1,"has_more":false,"comments":[{"id":"4272e6b5","author":{"name":"gohort_agent","karma":709,"lastActive":"2026-09-05T06:01:12.001Z","createdAt":"2026-07-15T16:49:27.914Z"},"content":"first","created_at":"2026-09-02T10:00:00Z","parent_id":null}]}`

// A real change: a second comment.
const watchCommentsB = `HTTP 200 OK
{"count":2,"has_more":false,"comments":[{"id":"4272e6b5","author":{"name":"gohort_agent","karma":709,"lastActive":"2026-09-05T06:01:12.001Z","createdAt":"2026-07-15T16:49:27.914Z"},"content":"first","created_at":"2026-09-02T10:00:00Z","parent_id":null},{"id":"9f9f9f9f","author":{"name":"ClawdClawderberg","karma":12,"lastActive":"2026-09-05T06:00:00Z"},"content":"finally a reply","created_at":"2026-09-05T05:59:00Z","parent_id":null}]}`

// A presence timestamp changing is not a change. The watch used to fire on
// exactly this every cycle and wake the agent for nothing.
func TestWatchComparableIgnoresPresenceTimestamps(t *testing.T) {
	a, again := watchComparable(watchCommentsA), watchComparable(watchCommentsAgain)
	if a != again {
		t.Fatalf("lastActive alone must not change the comparable form:\n%s\n---\n%s", a, again)
	}
	if sha256Sum(a) != sha256Sum(again) {
		t.Error("the hash must agree too")
	}
	if strings.Contains(a, "lastActive") {
		t.Error("the presence field must be gone from the comparable form")
	}
	if !strings.Contains(a, "created_at") || !strings.Contains(a, "createdAt") {
		t.Error("creation times are content and must survive")
	}
}

// A new element is a change, and the diff names that element alone.
func TestWatchComparableDiffNamesTheNewElement(t *testing.T) {
	a, b := watchComparable(watchCommentsA), watchComparable(watchCommentsB)
	if a == b {
		t.Fatal("a new comment must change the comparable form")
	}
	d := diffLines(a, b)
	if !strings.Contains(d, `+ {"author":{"karma":12,"name":"ClawdClawderberg"},"content":"finally a reply"`) {
		t.Errorf("the added line must be the new comment, got:\n%s", d)
	}
	if strings.Contains(d, "4272e6b5") {
		t.Errorf("the unchanged comment must not appear in the diff:\n%s", d)
	}
	if !strings.Contains(d, `+ "count": 2`) || !strings.Contains(d, `- "count": 1`) {
		t.Errorf("the count member changed and must be shown:\n%s", d)
	}
	if strings.Contains(d, "+ ,") || strings.Contains(d, "- ,") {
		t.Errorf("separator lines must not appear in the diff:\n%s", d)
	}
	if !addedLinesContain(a, b, "ClawdClawderberg") {
		t.Error("match_new scoping must see the new author on the added line")
	}
}

// The status line is kept, the body stays valid JSON for a format_script,
// and keys come out sorted so ordering churn cannot trip the hash either.
func TestWatchComparableStaysJSONWithStatus(t *testing.T) {
	c := watchComparable(watchCommentsA)
	status, body := SplitHTTPStatus(c)
	if status != "HTTP 200 OK" {
		t.Errorf("status line = %q", status)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("comparable body must still parse as JSON: %v\n%s", err, body)
	}
	reordered := `HTTP 200 OK
{"has_more":false,"comments":[{"parent_id":null,"content":"first","created_at":"2026-09-02T10:00:00Z","id":"4272e6b5","author":{"createdAt":"2026-07-15T16:49:27.914Z","karma":709,"name":"gohort_agent"}}],"count":1}`
	if watchComparable(reordered) != c {
		t.Error("key order must not matter")
	}
}

// A feed lists newest first. Prepending must leave every existing element
// line untouched, so the diff is the new element and nothing else.
func TestWatchComparablePrependIsOneAddedLine(t *testing.T) {
	old := `[{"id":"b","text":"older"},{"id":"a","text":"oldest"}]`
	now := `[{"id":"c","text":"newest"},{"id":"b","text":"older"},{"id":"a","text":"oldest"}]`
	d := diffLines(watchComparable(old), watchComparable(now))
	if d != `+ {"id":"c","text":"newest"}` {
		t.Errorf("diff = %q", d)
	}
}

// Anything that is not JSON is left exactly as it was.
func TestWatchComparablePassesThroughText(t *testing.T) {
	for _, s := range []string{"SouthPawn has left TeamSpeak.\nnobody: online", "", "HTTP 500 Internal Server Error\n<html>boom</html>", "{not json"} {
		if watchComparable(s) != s {
			t.Errorf("%q must pass through untouched", s)
		}
	}
}

// The card a person sees in the thread names the new comment the way a
// person would, and never shows the record's JSON.
func TestWatchCardTextReadsLikeAMessage(t *testing.T) {
	card := watchCardText("molty-profile-bug-replies", watchComparable(watchCommentsA), watchComparable(watchCommentsB))
	for _, want := range []string{
		`Watch "molty-profile-bug-replies" changed.`,
		"New (1):",
		`• ClawdClawderberg — "finally a reply" (9f9f9f9f, 2026-09-05 05:59 UTC)`,
		"Changed (1):",
		"• count: 1 → 2",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	for _, never := range []string{`{"author"`, "Current output", "HTTP 200", "4272e6b5", "Gone"} {
		if strings.Contains(card, never) {
			t.Errorf("card must not contain %q:\n%s", never, card)
		}
	}
}

// Plain-text watches keep their own words; a change with no line-level
// difference still says something a person can act on.
func TestWatchCardTextForPlainAndFlatChanges(t *testing.T) {
	card := watchCardText("ts3", "SouthPawn: online\nnobody else", "SouthPawn: online\nWiWee: online")
	if !strings.Contains(card, "New (1):\n• WiWee: online") || !strings.Contains(card, "Gone (1):\n• nobody else") {
		t.Errorf("plain lines must be shown as they are:\n%s", card)
	}
	flat := watchCardText("x", "same\n", "same\n")
	if !strings.Contains(flat, "no line stands out") {
		t.Errorf("a change with no line difference must still say so: %q", flat)
	}
}

// The card rides the wake's context, so the waker in another package can
// store it in place of the prompt.
func TestWatchWakeCarriesTheCard(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	result := watchCommentsA
	RegisterWatchToolInvoker(func(owner, agentID, toolName string, args map[string]any) (string, error) {
		return result, nil
	})
	defer RegisterWatchToolInvoker(nil)
	var gotCard, gotSummary string
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		gotCard, gotSummary = EventCardFromContext(ctx), summary
		return true, ""
	})
	defer RegisterEventWaker(nil)
	m := EventMonitor{Owner: "u", Name: "molty", Kind: EventKindWatch, ToolName: "moltbook", Notify: EventNotifyChannel}
	SaveEventMonitor(db, m)
	executeWatchPoll(context.Background(), db, m) // baseline
	result = watchCommentsB
	executeWatchPoll(context.Background(), db, m)
	if !strings.Contains(gotCard, `ClawdClawderberg — "finally a reply"`) {
		t.Errorf("the waker must receive the readable card, got %q", gotCard)
	}
	if !strings.Contains(gotSummary, "Current output") || !strings.Contains(gotSummary, "finally a reply") {
		t.Errorf("the model's summary keeps the diff and payload, got %q", gotSummary)
	}
	if EventCardFromContext(context.Background()) != "" {
		t.Error("a context without a card must read as none")
	}
}

// A watch pointed at something that stopped moving stops polling — paused and
// kept, never deleted, with a row in the ledger so the stop is visible.
func TestAnIdleWatchPausesItselfVisibly(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	RegisterWatchToolInvoker(func(owner, agentID, toolName string, args map[string]any) (string, error) {
		return "nothing has changed here since July", nil
	})
	defer RegisterWatchToolInvoker(nil)

	long := time.Now().Add(-45 * 24 * time.Hour)
	m := EventMonitor{Owner: "u", Name: "frozen-thread", Kind: EventKindWatch, ToolName: "moltbook",
		Notify: EventNotifyChannel, Created: long, LastFired: long, SchedulerID: "sched-1"}
	SaveEventMonitor(db, m)

	executeWatchPoll(context.Background(), db, m) // first poll records the baseline
	if got, _ := GetEventMonitor(db, "u", "frozen-thread"); got.Paused {
		t.Fatal("the baseline poll must not pause anything — it has nothing to compare yet")
	}
	executeWatchPoll(context.Background(), db, m) // second poll: nothing changed
	got, _ := GetEventMonitor(db, "u", "frozen-thread")
	if !got.Paused {
		t.Fatal("a watch with nothing to say for 45 days must pause itself")
	}
	if got.Broken {
		t.Error("nothing is broken — it must not be marked so")
	}
	if got.SchedulerID != "" || !got.NextCheck.IsZero() {
		t.Error("a paused watch must stop being scheduled")
	}
	var found string
	for _, r := range ListRuns(db, "u", RunFilter{}) {
		if strings.Contains(r.Summary, "no change to report") {
			found = r.Summary
			if r.Status != RunAttention {
				t.Errorf("the row must ask for attention, got %q", r.Status)
			}
		}
	}
	if found == "" {
		t.Error("the pause must leave a ledger row where the owner looks")
	}
}

// The clock and the exemptions: a watch that changed recently keeps running,
// and the guard never touches a paused, broken, or non-watch monitor.
func TestIdleWatchDueRespectsTheClockAndTheExemptions(t *testing.T) {
	now := time.Now()
	old, recent := now.Add(-40*24*time.Hour), now.Add(-2*24*time.Hour)
	base := EventMonitor{Kind: EventKindWatch, Created: old, LastFired: old}

	if !idleWatchDue(base, now, 30) {
		t.Error("40 days quiet past a 30-day guard is due")
	}
	if idleWatchDue(base, now, 0) {
		t.Error("0 disables the guard")
	}
	fresh := base
	fresh.LastFired = recent
	if idleWatchDue(fresh, now, 30) {
		t.Error("a watch that fired two days ago is not idle")
	}
	// Never fired: the clock runs from creation, so a watch pointed at
	// something that never moves is exactly what this catches.
	never := EventMonitor{Kind: EventKindWatch, Created: old}
	if !idleWatchDue(never, now, 30) {
		t.Error("a watch that has never fired ages from creation")
	}
	if idleWatchDue(EventMonitor{Kind: EventKindWatch}, now, 30) {
		t.Error("with no clock at all the guard must leave it alone")
	}
	for _, ex := range []EventMonitor{
		{Kind: EventKindWatch, Created: old, LastFired: old, Paused: true},
		{Kind: EventKindWatch, Created: old, LastFired: old, Broken: true},
		{Kind: EventKindPoll, Created: old, LastFired: old},
	} {
		if idleWatchDue(ex, now, 30) {
			t.Errorf("exempt monitor was judged idle: %+v", ex)
		}
	}
}

// --- fire allowance ----------------------------------------------------------

// TestAMonitorStopsWhenItHasFiredItsLimit is the behavior the record could not
// express before: OneShot was the only stopping condition, and only
// await_result ever set it. A user who asked to be told the next two times got
// a monitor that was created, reported as set up, and then polled forever.
func TestAMonitorStopsWhenItHasFiredItsLimit(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return true, ""
	})
	defer RegisterEventWaker(nil)

	m := EventMonitor{Name: "status", Owner: "craig", Kind: EventKindHTTP, MaxFires: 2, IntervalSeconds: 300, NextCheck: time.Now().Add(5 * time.Minute)}
	SaveEventMonitor(db, m)

	FireEventMonitor(context.Background(), db, m, "status went red")
	cur, _ := GetEventMonitor(db, "craig", "status")
	if cur.Paused {
		t.Fatal("it stopped on the FIRST fire of a two-fire allowance")
	}
	if got := MonitorFiresUsed(cur); got != 1 {
		t.Errorf("one fire spent, allowance says %d", got)
	}

	FireEventMonitor(context.Background(), db, m, "status went red again")
	cur, _ = GetEventMonitor(db, "craig", "status")
	if !cur.Paused {
		t.Error("the monitor kept watching past the limit it was created with")
	}
	if !cur.NextCheck.IsZero() {
		t.Error("a stopped monitor still has a next check scheduled")
	}
	// Stopped, not broken and not deleted: nothing failed, and the owner can
	// still see it, read why, and turn it back on.
	if cur.Broken {
		t.Error("reaching an agreed limit is not a fault")
	}
	if _, ok := GetEventMonitor(db, "craig", "status"); !ok {
		t.Error("the monitor was deleted rather than kept")
	}

	// And the stop says so where the owner looks, rather than the monitor just
	// going quiet.
	said := false
	for _, r := range ListRuns(db, "craig", RunFilter{}) {
		if strings.Contains(r.Summary, "limit it was created with") {
			said = true
		}
	}
	if !said {
		t.Error("nothing in the run ledger says the monitor stopped or why")
	}
}

// TestAnUndeliveredFireStillSpendsTheAllowance: counting only fires that
// reached somebody would leave a monitor with broken delivery running without
// a bound — the exact shape the limit exists to end. The ledger row already
// records which of the two happened.
func TestAnUndeliveredFireStillSpendsTheAllowance(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return false, "nowhere to deliver"
	})
	defer RegisterEventWaker(nil)

	m := EventMonitor{Name: "status", Owner: "craig", Kind: EventKindHTTP, MaxFires: 1}
	SaveEventMonitor(db, m)
	FireEventMonitor(context.Background(), db, m, "tripped")

	cur, _ := GetEventMonitor(db, "craig", "status")
	if !cur.Paused {
		t.Error("an undelivered fire left the allowance unspent, so the monitor runs on")
	}
}

// TestAnUnboundedMonitorKeepsWatching — the default is unchanged. Every
// monitor that existed before this had no limit, and must still have none.
func TestAnUnboundedMonitorKeepsWatching(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return true, ""
	})
	defer RegisterEventWaker(nil)

	m := EventMonitor{Name: "roster", Owner: "craig", Kind: EventKindWatch}
	SaveEventMonitor(db, m)
	for i := 0; i < 4; i++ {
		FireEventMonitor(context.Background(), db, m, "changed")
	}
	cur, _ := GetEventMonitor(db, "craig", "roster")
	if cur.Paused {
		t.Error("a monitor with no limit stopped itself")
	}
	if cur.FireCount != 4 {
		t.Errorf("fires are counted even with no limit (it is what a later limit measures): got %d", cur.FireCount)
	}
	if MonitorFireLabel(cur) != "" {
		t.Errorf("an unbounded monitor has no allowance to show: %q", MonitorFireLabel(cur))
	}
}

// TestResumingAStoppedMonitorGivesItAFreshAllowance. Turning a finished
// monitor back on means "watch again", not "fire once more and stop" — which
// is what comparing the lifetime count to the limit would have meant.
func TestResumingAStoppedMonitorGivesItAFreshAllowance(t *testing.T) {
	spent := EventMonitor{Name: "status", Owner: "craig", Kind: EventKindHTTP, MaxFires: 2, FireCount: 2}
	if !MonitorFiredOut(spent) {
		t.Fatal("two of two fires is spent")
	}
	if !RearmMonitorFires(&spent) {
		t.Fatal("resume did not restart the allowance")
	}
	if MonitorFiredOut(spent) {
		t.Error("the monitor is still out of fires immediately after being resumed")
	}
	if spent.FireCount != 2 {
		t.Errorf("the lifetime count was reset instead of the allowance: %d", spent.FireCount)
	}
	if got := MonitorFireLabel(spent); got != "fired 0 of 2" {
		t.Errorf("the fresh allowance reads %q", got)
	}

	// An ordinary unpause of a monitor with fires left keeps them.
	partial := EventMonitor{Name: "status", Owner: "craig", MaxFires: 3, FireCount: 1}
	if RearmMonitorFires(&partial) {
		t.Error("an unspent allowance was needlessly restarted")
	}
	if got := MonitorFireLabel(partial); got != "fired 1 of 3" {
		t.Errorf("remaining fires misreported as %q", got)
	}
}

// TestAOneShotAwaitStillRemovesItself: the transient await keeps its own
// delete-on-fire path. It is not a limit — nobody wants an await's corpse in
// the monitor list.
func TestAOneShotAwaitStillRemovesItself(t *testing.T) {
	db := memDB(t)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return true, ""
	})
	defer RegisterEventWaker(nil)

	m := EventMonitor{Name: "await_read_chat_x", Owner: "craig", Kind: EventKindWatch, OneShot: true}
	SaveEventMonitor(db, m)
	FireEventMonitor(context.Background(), db, m, "Alex replied")

	if _, ok := GetEventMonitor(db, "craig", "await_read_chat_x"); ok {
		t.Error("a one-shot await survived its fire")
	}
}

// --- failing checks ----------------------------------------------------------

// TestAFailingHTTPPollStopsInsteadOfRetryingForever. Only the watch kind used
// to count its failures; an http_poll logged the error and returned, so a
// monitor pointed at a hostname that cannot resolve retried every interval
// with nothing to show for it — no ledger row, no state change, and a console
// row still reading "active" with a freshly stamped last-checked time. Live,
// that ran for three hours and the only symptom was that it never fired.
func TestAFailingHTTPPollStopsInsteadOfRetryingForever(t *testing.T) {
	db := memDB(t)
	// Port 1 on loopback refuses instantly: a real failure with no network.
	m := EventMonitor{
		Name: "dead-endpoint", Owner: "craig", Kind: EventKindHTTP,
		URL: "http://127.0.0.1:1/status", CompareOp: ">", Threshold: "1",
		IntervalSeconds: 300,
	}
	SaveEventMonitor(db, m)

	for i := 1; i < monitorFailureThreshold; i++ {
		executeHTTPPoll(context.Background(), db, m)
		cur, _ := GetEventMonitor(db, "craig", "dead-endpoint")
		if cur.Broken {
			t.Fatalf("parked after %d failure(s); the threshold is %d and a blip must self-heal", i, monitorFailureThreshold)
		}
		if cur.ConsecutiveFailures != i {
			t.Errorf("failure %d was not counted: streak is %d", i, cur.ConsecutiveFailures)
		}
	}

	executeHTTPPoll(context.Background(), db, m)
	cur, ok := GetEventMonitor(db, "craig", "dead-endpoint")
	if !ok {
		t.Fatal("the monitor was deleted rather than parked")
	}
	if !cur.Broken || !cur.Paused {
		t.Errorf("still checking after %d consecutive failures: broken=%v paused=%v", monitorFailureThreshold, cur.Broken, cur.Paused)
	}
	// The reason has to name what failed, or "needs attention" is another
	// dead end for whoever reads the row.
	if !strings.Contains(cur.BrokenReason, "127.0.0.1") {
		t.Errorf("the broken reason does not say what failed: %q", cur.BrokenReason)
	}

	// And it says so once, in Activity, where the owner is actually looking.
	rows := 0
	for _, r := range ListRuns(db, "craig", RunFilter{}) {
		if strings.Contains(r.Summary, "Stopped checking after") {
			rows++
			if r.Status != RunAttention {
				t.Errorf("a monitor that gave up is attention, got %q", r.Status)
			}
		}
	}
	if rows != 1 {
		t.Errorf("want exactly one row for the stop, got %d — one per failed check would bury the feed", rows)
	}
}

// TestABadComparisonCountsAsAFailure: a threshold that cannot be compared was
// written when the monitor was created and will not fix itself on the next
// interval, so retrying it forever is the same silence by another route.
func TestABadComparisonCountsAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-a-number"))
	}))
	defer srv.Close()

	db := memDB(t)
	m := EventMonitor{
		Name: "bad-compare", Owner: "craig", Kind: EventKindHTTP,
		URL: srv.URL, CompareOp: ">", Threshold: "10",
	}
	SaveEventMonitor(db, m)
	for i := 0; i < monitorFailureThreshold; i++ {
		executeHTTPPoll(context.Background(), db, m)
	}
	cur, _ := GetEventMonitor(db, "craig", "bad-compare")
	if !cur.Broken {
		t.Error("a comparison that can never succeed retried forever")
	}
}

// TestAGoodCheckClearsTheFailureStreak — the threshold counts CONSECUTIVE
// failures, so an endpoint that blips and recovers keeps watching.
func TestAGoodCheckClearsTheFailureStreak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("42"))
	}))
	defer srv.Close()
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		return true, ""
	})
	defer RegisterEventWaker(nil)

	db := memDB(t)
	m := EventMonitor{
		Name: "flaky", Owner: "craig", Kind: EventKindHTTP,
		URL: srv.URL, CompareOp: ">", Threshold: "10",
		ConsecutiveFailures: monitorFailureThreshold - 1,
	}
	SaveEventMonitor(db, m)

	executeHTTPPoll(context.Background(), db, m)
	cur, _ := GetEventMonitor(db, "craig", "flaky")
	if cur.ConsecutiveFailures != 0 {
		t.Errorf("a good check left the streak at %d", cur.ConsecutiveFailures)
	}
	if cur.Broken || cur.Paused {
		t.Error("a monitor that recovered was parked anyway")
	}
	if !cur.LastBreached {
		t.Error("the recovering check did not fire the crossing it observed")
	}
}
