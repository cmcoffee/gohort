package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

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
	for i := 0; i < watchFailureThreshold; i++ {
		executeWatchPoll(context.Background(), db, m)
	}
	got, _ := GetEventMonitor(db, "u", "w")
	if !got.Broken || !got.Paused {
		t.Fatalf("after %d consecutive failures the monitor must be broken+paused; broken=%v paused=%v failures=%d",
			watchFailureThreshold, got.Broken, got.Paused, got.ConsecutiveFailures)
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
