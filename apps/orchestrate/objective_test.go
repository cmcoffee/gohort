package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestObjectiveOutcomeRules pins the whole decision table, because every arm of
// it is a different promise to the owner: a met goal stops quietly, an unmet one
// with budget left says where it stands, and one that runs out of attempts stops
// LOUDLY. Getting the last two the same way round is how an objective would fail
// in silence.
func TestObjectiveOutcomeRules(t *testing.T) {
	met := objectiveVerdict{Met: true, Reason: "the post is live and its URL is in the thread"}
	notYet := objectiveVerdict{Reason: "published, but the thread reply carried the title, not the URL"}

	cases := []struct {
		name             string
		v                objectiveVerdict
		judged           bool
		attempt, max     int
		wantStop         bool
		wantStalled      bool
		wantLineContains string
	}{
		{"met retires", met, true, 2, 5, true, false, "objective met"},
		{"met on the last allowed attempt still retires, not stalls", met, true, 5, 5, true, false, "objective met"},
		{"unmet with budget left continues", notYet, true, 2, 5, false, false, "not yet"},
		{"unmet at the bound stalls", notYet, true, 5, 5, true, true, "STALLED"},
		{"unjudged counts as an attempt", objectiveVerdict{}, false, 2, 5, false, false, "could not be judged"},
		{"unjudged at the bound stalls", objectiveVerdict{}, false, 5, 5, true, true, "STALLED"},
		{"no bound never stalls", notYet, true, 99, 0, false, false, "not yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, stop, stalled := objectiveOutcome(c.v, c.judged, c.attempt, c.max)
			if stop != c.wantStop || stalled != c.wantStalled {
				t.Errorf("stop=%v stalled=%v, want stop=%v stalled=%v (line %q)", stop, stalled, c.wantStop, c.wantStalled, line)
			}
			if !strings.Contains(line, c.wantLineContains) {
				t.Errorf("line %q does not mention %q", line, c.wantLineContains)
			}
			if c.judged && c.v.Reason != "" && !strings.Contains(line, c.v.Reason) {
				t.Errorf("line %q dropped the reason the owner needs: %q", line, c.v.Reason)
			}
		})
	}

	// A stall is never a quiet retirement: it must be distinguishable from a met
	// goal by the line alone, since that line is what the card shows.
	stalledLine, _, _ := objectiveOutcome(notYet, true, 3, 3)
	metLine, _, _ := objectiveOutcome(met, true, 3, 3)
	if strings.Contains(stalledLine, "objective met") || stalledLine == metLine {
		t.Errorf("a stalled objective reads like a met one: %q vs %q", stalledLine, metLine)
	}
}

// TestObjectiveEvidenceLeadsWithActions: the checker is told the actions are the
// evidence and the report is a claim about them. A fire that reported success
// while calling nothing is the exact shape this has to catch (nine reads
// reported as three posts, live on this path), so both halves have to reach the
// model — and the goal has to arrive with them or there is nothing to check.
func TestObjectiveEvidenceLeadsWithActions(t *testing.T) {
	msg := objectiveEvidenceMessage(objectiveEvidence{
		Objective:   "the blog post is published and its URL posted to the thread",
		Reply:       "Published the post and linked it.",
		ToolCalls:   []string{"moltbook/get_feed", "moltbook/get_feed"},
		ToolErrors:  1,
		Attempt:     3,
		MaxAttempts: 5,
	})
	for _, want := range []string{
		"the blog post is published",    // the goal
		"moltbook/get_feed",             // the actions, with their grouped action
		"ACTIONS THAT FAILED: 1",        // failures, so "tried and refused" is visible
		"Published the post and linked", // the claim
		"ATTEMPT: 3 of 5",               // where it stands
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("evidence is missing %q:\n%s", want, msg)
		}
	}
	// An attempt that ran nothing must say so rather than leave the line blank,
	// or "no actions" reads as "actions unknown".
	bare := objectiveEvidenceMessage(objectiveEvidence{Objective: "g", Reply: "done!", Attempt: 1})
	if !strings.Contains(bare, "IN ORDER: none") {
		t.Errorf("an attempt with no actions must say none:\n%s", bare)
	}
	if !strings.Contains(objectiveJudgeSysPrompt, "ACTIONS are the evidence") {
		t.Error("the checker is no longer told the actions outrank the report — a reply claiming success would pass")
	}
}

// TestJudgeObjectiveOnlyPassesOnAClearMet: the bool is "there is an opinion",
// and everything that is not a readable MET/NOT_YET must return no opinion. The
// caller counts no-opinion as an unmet attempt, so failing open here would
// retire a task whose goal was never reached — the one outcome nobody notices.
func TestJudgeObjectiveOnlyPassesOnAClearMet(t *testing.T) {
	ev := objectiveEvidence{Objective: "the post is live", Reply: "done", Attempt: 1}
	cases := []struct {
		name       string
		reply      string
		wantJudged bool
		wantMet    bool
	}{
		{"met", `{"verdict":"MET","reason":"the post is live"}`, true, true},
		{"not yet", `{"verdict":"NOT_YET","reason":"nothing was published"}`, true, false},
		{"spaced spelling", `{"verdict":"not yet","reason":"nothing was published"}`, true, false},
		{"unknown verdict word", `{"verdict":"MAYBE","reason":"unsure"}`, false, false},
		{"empty verdict", `{"verdict":"","reason":"unsure"}`, false, false},
		{"not json at all", `I think it is done.`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			T := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: c.reply}}}
			v, judged := T.judgeObjective(context.Background(), ev)
			if judged != c.wantJudged || v.Met != c.wantMet {
				t.Errorf("judged=%v met=%v, want judged=%v met=%v", judged, v.Met, c.wantJudged, c.wantMet)
			}
			if judged && strings.TrimSpace(v.Reason) == "" {
				t.Error("an opinion with no reason gives the card and the next attempt nothing to show")
			}
		})
	}

	// No LLM configured is no opinion, not a pass.
	if _, judged := (&OrchestrateApp{}).judgeObjective(context.Background(), ev); judged {
		t.Error("an app with no LLM must not return a verdict")
	}
}

// TestObjectiveToolLabelsNameTheAction: a grouped tool hides reading and writing
// behind one name, so a goal about posting would be "met" by a list of reads if
// the labels dropped the action.
func TestObjectiveToolLabelsNameTheAction(t *testing.T) {
	labels, failed := objectiveToolLabels([]PersistedToolCall{
		{Name: "moltbook", Args: map[string]any{"action": "get_feed"}},
		{Name: "moltbook", Args: map[string]any{"action": "create_post"}, Err: "401 unauthorized"},
		{Name: "web_search"},
	})
	want := []string{"moltbook/get_feed", "moltbook/create_post", "web_search"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("labels = %v, want %v", labels, want)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

// TestObjectiveAttemptsBlock: what makes a fifth attempt different from a
// first. Absent on the first fire and on any task that is not an objective;
// otherwise every earlier reason, oldest first, so the model reads them in the
// order they happened and the newest sits nearest its own turn.
func TestObjectiveAttemptsBlock(t *testing.T) {
	base := orchUpdatePayload{
		Username: "u",
		Until:    "the post is published and its URL is in the thread",
	}

	if got := objectiveAttemptsBlock(base.objective()); got != "" {
		t.Errorf("the first attempt has nothing to report, got %q", got)
	}
	notObjective := base
	notObjective.Until = ""
	notObjective.Attempts = []ObjectiveAttempt{{At: "2026-09-04T16:00:00Z", Reason: "whatever"}}
	if got := objectiveAttemptsBlock(notObjective.objective()); got != "" {
		t.Errorf("an ordinary recurring task gets no objective block, got %q", got)
	}

	p := base
	p.MaxAttempts = 5
	p.Attempts = []ObjectiveAttempt{
		{At: "2026-09-04T16:00:00Z", Reason: "drafted but never published (create_post returned 401)"},
		{At: "2026-09-05T16:00:00Z", Reason: "published, but the thread reply carried the title, not the URL"},
		{At: "2026-09-06T16:00:00Z", Reason: "URL posted to the wrong thread"},
	}
	block := objectiveAttemptsBlock(p.objective())
	for _, want := range []string{
		"the post is published", // the goal, restated for this fire
		"Attempts so far: 3.",
		"1. ", "2. ", "3. ",
		"create_post returned 401",
		"carried the title, not the URL",
		"URL posted to the wrong thread",
		"Do not repeat an attempt that already failed",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block is missing %q:\n%s", want, block)
		}
	}
	// Oldest first: the newest reason must be the last one mentioned, or the
	// model reads the history backwards.
	if strings.Index(block, "create_post returned 401") > strings.Index(block, "wrong thread") {
		t.Errorf("attempts are not oldest-first:\n%s", block)
	}

	// The count, never the bound. After a Resume the allowance restarts while
	// the history keeps its length, so a denominator would be a lie — and
	// telling the attempting model it is nearly out of tries is pressure to
	// declare success, which is the one thing the checker exists to catch.
	if strings.Contains(block, "of 5") {
		t.Errorf("the block leaks how close the task is to being cut off:\n%s", block)
	}
}

// TestObjectiveAttemptNumberRestartsAfterResume: the bound is measured against
// the CURRENT allowance, not the lifetime fire count. Without that, an owner
// who read a stall, fixed what it named and hit Resume would get exactly one
// fire and the same refusal.
func TestObjectiveAttemptNumberRestartsAfterResume(t *testing.T) {
	if n := objectiveAttemptNumber(orchUpdatePayload{}); n != 1 {
		t.Errorf("a fresh objective is on attempt %d, want 1", n)
	}
	if n := objectiveAttemptNumber(orchUpdatePayload{FireCount: 4}); n != 5 {
		t.Errorf("the fifth fire is attempt %d, want 5", n)
	}

	// Stalled at 5 of 5, then resumed: the handler moves the base to the fire
	// count, so the next fire is attempt 1 of a fresh allowance.
	resumed := orchUpdatePayload{FireCount: 5, AttemptsBase: 5, MaxAttempts: 5}
	if n := objectiveAttemptNumber(resumed); n != 1 {
		t.Fatalf("a resumed objective is on attempt %d — it would stall on its first fire", n)
	}
	if _, stop, stalled := objectiveOutcome(objectiveVerdict{Reason: "still not published"}, true,
		objectiveAttemptNumber(resumed), resumed.MaxAttempts); stop || stalled {
		t.Error("a resumed objective stalled again on its first fire")
	}

	// A base ahead of the count (an edited or restored payload) reads as a
	// fresh allowance rather than a negative attempt.
	if n := objectiveAttemptNumber(orchUpdatePayload{FireCount: 2, AttemptsBase: 9}); n != 1 {
		t.Errorf("a base ahead of the fire count gave attempt %d, want 1", n)
	}
}

// TestObjectiveStateLabel is what the console row and the tool's listing show:
// enough to know whether to intervene, without opening the thread.
func TestObjectiveStateLabel(t *testing.T) {
	if got := objectiveStateLabel(objectiveRun{}); got != "" {
		t.Errorf("an ordinary recurring task has no objective state, got %q", got)
	}
	p := orchUpdatePayload{Until: "the post is live"}
	if got := objectiveStateLabel(p.objective()); !strings.Contains(got, "no attempts yet") {
		t.Errorf("a fresh objective should say it has not tried yet, got %q", got)
	}
	p.Attempts = []ObjectiveAttempt{{Reason: "create_post returned 401"}}
	got := objectiveStateLabel(p.objective())
	for _, want := range []string{"not yet", "1 attempt", "create_post returned 401"} {
		if !strings.Contains(got, want) {
			t.Errorf("state %q is missing %q", got, want)
		}
	}
}

// TestRecurringToolOffersTheObjectiveParams: stages 1 and 2 built machinery
// nothing could switch on. This is the parameter that makes an objective
// reachable from a conversation, so its absence is the whole feature missing.
func TestRecurringToolOffersTheObjectiveParams(t *testing.T) {
	params := (&chatTurn{}).recurringToolDef().Tool.Parameters
	until, ok := params["until"]
	if !ok {
		t.Fatal("the recurring tool no longer offers `until` — an objective cannot be authored")
	}
	if until.Type != "string" {
		t.Errorf("until is %q, want string", until.Type)
	}
	if !strings.Contains(until.Description, "OBJECTIVE") {
		t.Error("until's description does not tell the model what it turns the task into")
	}
	max, ok := params["max_attempts"]
	if !ok {
		t.Fatal("the recurring tool no longer offers `max_attempts`")
	}
	if max.Type != "integer" {
		t.Errorf("max_attempts is %q, want integer", max.Type)
	}
}

// TestNoteObjectiveAttemptBoundsAndIsolates: the history rides a payload that
// was COPIED from the firing one, so appending must not reach back into the
// original's backing array, and it must stay a note rather than growing into a
// transcript that rides every fire's prompt.
func TestNoteObjectiveAttemptBoundsAndIsolates(t *testing.T) {
	p := orchUpdatePayload{Attempts: []ObjectiveAttempt{{Reason: "first"}}}
	armed := p // the pre-armed successor: same backing array
	armed.Attempts = appendObjectiveAttempt(armed.Attempts, false, "second")
	if len(p.Attempts) != 1 || p.Attempts[0].Reason != "first" {
		t.Errorf("recording on the successor rewrote the firing payload: %+v", p.Attempts)
	}
	if len(armed.Attempts) != 2 || armed.Attempts[1].Reason != "second" {
		t.Fatalf("the attempt was not recorded: %+v", armed.Attempts)
	}
	if armed.Attempts[1].At == "" {
		t.Error("an attempt with no timestamp cannot be ordered or shown")
	}

	full := orchUpdatePayload{}
	for i := 0; i < objectiveAttemptsKept+4; i++ {
		full.Attempts = appendObjectiveAttempt(full.Attempts, false, fmt.Sprintf("reason %d", i))
	}
	if len(full.Attempts) != objectiveAttemptsKept {
		t.Errorf("kept %d attempts, want the last %d", len(full.Attempts), objectiveAttemptsKept)
	}
	// The ones dropped are the OLDEST: a bound that forgot the newest failure
	// would leave the next attempt repeating the mistake it just made.
	if last := full.Attempts[len(full.Attempts)-1].Reason; last != fmt.Sprintf("reason %d", objectiveAttemptsKept+3) {
		t.Errorf("the newest attempt was dropped; last is %q", last)
	}
	if first := full.Attempts[0].Reason; first != "reason 4" {
		t.Errorf("wrong window kept; first is %q", first)
	}
}

// TestStandingObjectiveSharesTheRecurringMachinery: the two scheduling
// surfaces keep their objective fields FLAT on their own records — an embedded
// struct would be nested by gob and silently change the shape of everything
// already stored — so they meet at a by-value view instead. If that view stops
// carrying a standing agent's fields, the Fleet path silently loses its
// objective while still accepting one.
func TestStandingObjectiveSharesTheRecurringMachinery(t *testing.T) {
	sa := StandingAgent{
		Owner: "u", Name: "nightly",
		Until:    "the report is filed",
		Attempts: []ObjectiveAttempt{{At: "2026-09-07T04:00:00Z", Reason: "the API refused"}},
	}
	o := standingObjective(sa)
	if o.Until != sa.Until || len(o.Attempts) != 1 || o.Username != "u" {
		t.Fatalf("the standing view lost fields: %+v", o)
	}

	// The same renderers the recurring path uses, on a standing agent.
	block := objectiveAttemptsBlock(o)
	for _, want := range []string{"the report is filed", "the API refused", "Do not repeat an attempt"} {
		if !strings.Contains(block, want) {
			t.Errorf("standing attempts block is missing %q:\n%s", want, block)
		}
	}
	if state := objectiveStateLabel(o); !strings.Contains(state, "not yet") {
		t.Errorf("standing state label reads %q", state)
	}
	// A standing agent with no goal gets neither, exactly like a plain
	// recurring task.
	plain := standingObjective(StandingAgent{Owner: "u", Name: "nightly"})
	if objectiveAttemptsBlock(plain) != "" || objectiveStateLabel(plain) != "" {
		t.Error("an ordinary standing agent should carry no objective text")
	}
}

// TestCreateStandingAgentOffersTheObjective: the Fleet path is the ONLY
// scheduling path a Fleet agent has — it is not given the recurring tool — so
// without these parameters an objective is unreachable for exactly the agents
// most likely to be handed a goal.
func TestCreateStandingAgentOffersTheObjective(t *testing.T) {
	var params map[string]ToolParam
	for _, td := range operatorManagementTools(nil, "") {
		if td.Tool.Name == "create_standing_agent" {
			params = td.Tool.Parameters
		}
	}
	if params == nil {
		t.Fatal("create_standing_agent is no longer registered")
	}
	until, ok := params["until"]
	if !ok {
		t.Fatal("create_standing_agent no longer offers `until` — a Fleet agent cannot be given a goal")
	}
	if !strings.Contains(until.Description, "OBJECTIVE") {
		t.Error("until's description does not say what it turns the schedule into")
	}
	if _, ok := params["max_attempts"]; !ok {
		t.Fatal("create_standing_agent no longer offers `max_attempts`")
	}
}

// --- monitor objectives -------------------------------------------------------

// TestTheObservationCheckerDoesNotAskWhatWasDONE is why a monitor gets its own
// prompt instead of reusing the attempt checker. That one treats the ACTIONS as
// the evidence and is told an attempt which ran none has almost certainly not
// reached its goal — the rule that stops an agent claiming success it did not
// earn. A monitor fire runs no actions by design: it watched something. Through
// the attempt checker, every fire would look like an attempt that did nothing.
func TestTheObservationCheckerDoesNotAskWhatWasDONE(t *testing.T) {
	for _, banned := range []string{"ACTIONS", "attempt ran", "actions the attempt"} {
		if strings.Contains(objectiveObservationSysPrompt, banned) {
			t.Errorf("the observation checker asks about actions (%q) — a monitor takes none", banned)
		}
	}
	// It has to say so positively, or a model that has seen a thousand
	// "did it do the work" checkers will supply the rule itself.
	if !strings.Contains(objectiveObservationSysPrompt, "takes no actions") {
		t.Error("the prompt never tells the checker that no actions is the normal case")
	}
	if !strings.Contains(objectiveObservationSysPrompt, "OBSERVATION is the evidence") {
		t.Error("the prompt does not name what the evidence actually is")
	}
	// And the shared rules that make a verdict usable are still there.
	for _, want := range []string{"MET", "NOT_YET", "ONE short sentence", "JSON only"} {
		if !strings.Contains(objectiveObservationSysPrompt, want) {
			t.Errorf("the observation checker dropped %q", want)
		}
	}
}

// TestMonitorObservationMessageCarriesTheChange: the verdict turns entirely on
// this message, so it must carry the condition and what was actually seen.
func TestMonitorObservationMessageCarriesTheChange(t *testing.T) {
	msg := monitorObservationMessage("the PR is merged", "PR #12: state changed open → merged", 3)
	for _, want := range []string{"STOPPING CONDITION", "the PR is merged", "FIRE: 3", "WHAT THE MONITOR OBSERVED", "open → merged"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the evidence is missing %q:\n%s", want, msg)
		}
	}
}

// TestAMetConditionStopsTheMonitor is the end of the wire: judged met → the
// monitor is paused, kept, and says why.
func TestAMetConditionStopsTheMonitor(t *testing.T) {
	db := pinRootDB(t)
	m := EventMonitor{Name: "pr-12", Owner: "craig", Kind: EventKindWatch, Until: "the PR is merged"}
	SaveEventMonitor(db, m)

	T := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: `{"verdict":"MET","reason":"the PR shows state merged"}`}}}
	T.settleMonitorObjective(context.Background(), m, "PR #12: state changed open → merged")

	cur, ok := GetEventMonitor(db, "craig", "pr-12")
	if !ok {
		t.Fatal("the monitor was deleted rather than stopped")
	}
	if !cur.Paused {
		t.Error("the condition was met and the monitor kept watching")
	}
	if len(cur.Attempts) != 1 || !cur.Attempts[0].Met {
		t.Fatalf("the verdict was not recorded on the monitor: %+v", cur.Attempts)
	}
	if !strings.Contains(cur.Attempts[0].Reason, "state merged") {
		t.Errorf("the reason did not survive: %q", cur.Attempts[0].Reason)
	}
	if lbl := objectiveStateLabel(monitorObjective(cur)); !strings.Contains(lbl, "met") {
		t.Errorf("the listing would not show the goal as met: %q", lbl)
	}
}

// TestAnUnmetConditionLeavesTheMonitorWatching, and records the attempt so the
// row can say where the goal stands rather than only that it fired.
func TestAnUnmetConditionLeavesTheMonitorWatching(t *testing.T) {
	db := pinRootDB(t)
	m := EventMonitor{Name: "pr-12", Owner: "craig", Kind: EventKindWatch, Until: "the PR is merged"}
	SaveEventMonitor(db, m)

	T := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: `{"verdict":"NOT_YET","reason":"the PR is still open with one review pending"}`}}}
	T.settleMonitorObjective(context.Background(), m, "PR #12: a new review comment")

	cur, _ := GetEventMonitor(db, "craig", "pr-12")
	if cur.Paused {
		t.Error("an unmet condition stopped the monitor")
	}
	if len(cur.Attempts) != 1 || cur.Attempts[0].Met {
		t.Fatalf("the unmet verdict was not recorded: %+v", cur.Attempts)
	}

	// A verdict nobody could read is NOT a pass — the monitor keeps watching.
	// Failing open here would retire a monitor whose condition never happened,
	// which is the one outcome nobody would notice.
	unreadable := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: `I think so?`}}}
	unreadable.settleMonitorObjective(context.Background(), m, "PR #12: another comment")
	cur, _ = GetEventMonitor(db, "craig", "pr-12")
	if cur.Paused {
		t.Error("an unreadable verdict stopped the monitor")
	}
	if len(cur.Attempts) != 2 {
		t.Errorf("an unjudged fire is still an attempt and must be recorded: %+v", cur.Attempts)
	}
}

// TestAMonitorWithNoConditionIsNeverJudged: the model call is opt-in. A
// monitor without a stopping condition must not pay for one.
func TestAMonitorWithNoConditionIsNeverJudged(t *testing.T) {
	db := pinRootDB(t)
	m := EventMonitor{Name: "roster", Owner: "craig", Kind: EventKindWatch}
	SaveEventMonitor(db, m)

	// An LLM whose every answer is MET: if it is consulted at all, the monitor
	// stops and this test fails.
	T := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: `{"verdict":"MET","reason":"sure"}`}}}
	T.settleMonitorObjective(context.Background(), m, "the roster changed")

	cur, _ := GetEventMonitor(db, "craig", "roster")
	if cur.Paused || len(cur.Attempts) != 0 {
		t.Error("a monitor with no stopping condition was judged anyway")
	}
}

// TestCreateEventMonitorOffersTheStoppingControls: both are only reachable if
// the tool declares them. Their absence is the whole gap — a bound the user
// asked for that the record could not hold, reported back as set up.
func TestCreateEventMonitorOffersTheStoppingControls(t *testing.T) {
	var tool Tool
	for _, td := range operatorManagementTools(&ToolSession{Username: "craig"}, "agent-1") {
		if td.Tool.Name == "create_event_monitor" {
			tool = td.Tool
		}
	}
	if tool.Name == "" {
		t.Fatal("create_event_monitor is gone")
	}
	stop, ok := tool.Parameters["stop_after"]
	if !ok {
		t.Fatal("stop_after is gone — a bounded monitor cannot be created again")
	}
	if stop.Type != "number" {
		t.Errorf("stop_after is %q, want number", stop.Type)
	}
	until, ok := tool.Parameters["until"]
	if !ok {
		t.Fatal("until is gone — a monitor can no longer be given a stopping condition")
	}
	if until.Type != "string" {
		t.Errorf("until is %q, want string", until.Type)
	}
}

// TestTheTwoSchedulingToolsSayWhichJobIsTheirs. A live session spent ten
// create_event_monitor calls on "every 5 minutes, fetch X and report the
// value, stop after 2" — unconditional work on a clock, which is a standing
// agent with until/max_attempts, and which the agent had in its toolset the
// whole time. It never considered it. The pull got stronger when stop_after
// gave the monitor tool a home for "stop after 2" while the tool that was
// actually right said nothing about counts, so the routing lives in the
// descriptions and its absence is the fix missing.
func TestTheTwoSchedulingToolsSayWhichJobIsTheirs(t *testing.T) {
	tools := map[string]Tool{}
	for _, td := range operatorManagementTools(&ToolSession{Username: "craig"}, "agent-1") {
		tools[td.Tool.Name] = td.Tool
	}

	mon, ok := tools["create_event_monitor"]
	if !ok {
		t.Fatal("create_event_monitor is gone")
	}
	// It has to state the NEGATIVE case — and only that. The description is
	// re-sent on every turn the tool is in the catalog, so the detail lives
	// where it is read only when needed: the error the model gets while it is
	// already stuck (see below).
	for _, want := range []string{"NOT for work", "create_standing_agent"} {
		if !strings.Contains(mon.Description, want) {
			t.Errorf("the monitor tool no longer routes away from the job that is not its own: missing %q", want)
		}
	}

	// The redirect that matters most arrives at the moment of contortion, from
	// the real handler: an empty threshold IS the signature of having picked a
	// monitor for a schedule's job, and it was left empty twice in the live
	// session before the agent gave up on the kind.
	pinRootDB(t)
	var create func(map[string]any) (string, error)
	for _, td := range operatorManagementTools(&ToolSession{Username: "craig"}, "agent-1") {
		if td.Tool.Name == "create_event_monitor" {
			create = td.Handler
		}
	}
	_, err := create(map[string]any{
		"name": "unconditional", "kind": "http_poll",
		"url": "https://example.com/status", "compare_op": "contains", "threshold": "",
	})
	if err == nil {
		t.Fatal("an http_poll with no threshold was accepted")
	}
	if !strings.Contains(err.Error(), "create_standing_agent") {
		t.Errorf("the error names no alternative, so the next attempt is another guess: %v", err)
	}

	stand, ok := tools["create_standing_agent"]
	if !ok {
		t.Fatal("create_standing_agent is gone")
	}
	// And the right tool has to claim the job in the words people use for it,
	// including the finish line — otherwise it reads as "cron" and loses to
	// the tool that mentions stopping.
	for _, want := range []string{"RUNS on a clock", "until", "max_attempts", "stop after 2"} {
		if !strings.Contains(stand.Description, want) {
			t.Errorf("the standing-agent tool does not claim the job it is for: missing %q", want)
		}
	}

	// stop_after must not read as "run this N times".
	if p := mon.Parameters["stop_after"]; !strings.Contains(p.Description, "bounds the ALERTS") {
		t.Errorf("stop_after does not distinguish alerts from runs: %s", p.Description)
	}
}

// TestAStalledObjectiveIsNotAnUnlink. Two very different stops used to share
// one Broken bool, so the console could only offer the recovery for one of
// them: a schedule whose GOAL was not reached rendered "⚠ needs relink" and a
// picker asking the owner to re-point it at a live agent. Nothing was
// unlinked; the agent was never the problem.
func TestAStalledObjectiveIsNotAnUnlink(t *testing.T) {
	stalled := parkedStateLabel(ParkedByObjective, "objective not met after 3 attempt(s) — the post was never published")
	if strings.Contains(stalled, "relink") {
		t.Errorf("a stalled objective still tells the owner to relink: %q", stalled)
	}
	if !strings.Contains(stalled, "stalled") {
		t.Errorf("a stalled objective does not say what happened: %q", stalled)
	}
	// The reason still rides along — it is the whole content of the state.
	if !strings.Contains(stalled, "never published") {
		t.Errorf("the stall reason was dropped: %q", stalled)
	}

	// A missing dependency keeps the word that names its repair.
	gone := parkedStateLabel(ParkedByDependency, "its agent was deleted")
	if !strings.Contains(gone, "needs relink") {
		t.Errorf("a missing dependency lost its recovery: %q", gone)
	}
	// And the legacy path — brokenStateLabel is what every dependency guard
	// still calls — must be unchanged.
	if brokenStateLabel("its agent was deleted") != gone {
		t.Error("the dependency wording drifted between the two entry points")
	}
}

// TestParkCauseFallsBackForRecordsWrittenBeforeTheSplit: a schedule parked by
// the old code carries no cause, and must read as the only thing that could
// park one back then — a missing dependency — rather than as a blank.
func TestParkCauseFallsBackForRecordsWrittenBeforeTheSplit(t *testing.T) {
	if got := StandingParkCause(StandingAgent{Broken: true}); got != ParkedByDependency {
		t.Errorf("a legacy parked standing agent reads as %q", got)
	}
	if got := StandingParkCause(StandingAgent{Broken: true, BrokenCause: ParkedByObjective}); got != ParkedByObjective {
		t.Errorf("a stalled standing agent reads as %q", got)
	}
	if got := StandingParkCause(StandingAgent{}); got != "" {
		t.Errorf("a running standing agent has a park cause %q", got)
	}
	if got := recurringParkCause(orchUpdatePayload{Broken: true}); got != ParkedByDependency {
		t.Errorf("a legacy parked recurring task reads as %q", got)
	}
	if got := recurringParkCause(orchUpdatePayload{Broken: true, BrokenCause: ParkedByObjective}); got != ParkedByObjective {
		t.Errorf("a stalled recurring task reads as %q", got)
	}
}

// TestTheStallParkRecordsItsCause drives the real park helpers, so the wiring
// between "the objective stalled" and what the row shows is covered rather
// than assumed.
func TestTheStallParkRecordsItsCause(t *testing.T) {
	db := pinRootDB(t)
	SaveStandingAgent(db, StandingAgent{Name: "nightly", Owner: "craig", AgentID: "a1", Until: "the post is live"})

	if !MarkStandingAgentStalled(db, "craig", "nightly", "objective not met after 2 attempt(s) — nothing was published") {
		t.Fatal("the stall park did not find the schedule")
	}
	sa, _ := GetStandingAgent(db, "craig", "nightly")
	if !sa.Broken || !sa.Paused {
		t.Error("a stalled schedule must stop and be kept")
	}
	if StandingParkCause(sa) != ParkedByObjective {
		t.Errorf("the stall was recorded as %q", StandingParkCause(sa))
	}
	if sa.AgentID != "a1" {
		t.Errorf("the target was changed by a stall: %q", sa.AgentID)
	}
	if strings.Contains(parkedStateLabel(StandingParkCause(sa), sa.BrokenReason), "relink") {
		t.Error("the row still asks for a relink")
	}

	// Resume clears the cause along with the flag, and hands back a fresh
	// allowance — the recovery a stall actually wants.
	if !ClearStandingAgentBroken(db, "craig", "nightly") {
		t.Fatal("resume did not find the schedule")
	}
	sa, _ = GetStandingAgent(db, "craig", "nightly")
	if sa.Broken || sa.BrokenCause != "" || sa.UnmetCount != 0 {
		t.Errorf("resume left the stall behind: broken=%v cause=%q unmet=%d", sa.Broken, sa.BrokenCause, sa.UnmetCount)
	}

	// A dependency park still records the cause that offers a relink.
	MarkStandingAgentBroken(db, "craig", "nightly", "its agent was deleted")
	sa, _ = GetStandingAgent(db, "craig", "nightly")
	if StandingParkCause(sa) != ParkedByDependency {
		t.Errorf("a dependency park recorded %q", StandingParkCause(sa))
	}
}

// TestAMonitorWhoseChecksFailIsNotAnUnlinkEither — the same defect one surface
// over, and one I introduced: the failure breaker parks through the broken
// flag, so a hostname that will not resolve was rendering as "needs relink".
// No choice of agent fixes DNS.
func TestAMonitorWhoseChecksFailIsNotAnUnlinkEither(t *testing.T) {
	db := pinRootDB(t)
	SaveEventMonitor(db, EventMonitor{Name: "dead", Owner: "craig", Kind: EventKindHTTP, WakeAgent: "a1"})

	MarkEventMonitorFailing(db, "craig", "dead", "http_poll checks are failing: no such host")
	m, _ := GetEventMonitor(db, "craig", "dead")
	if m.StopCause() != MonitorStopFailing {
		t.Fatalf("the failure park recorded %q", m.StopCause())
	}
	if lbl := m.StopLabel(); strings.Contains(lbl, "relink") {
		t.Errorf("a failing check asks for a relink: %q", lbl)
	} else if !strings.Contains(lbl, "needs attention") || !strings.Contains(lbl, "no such host") {
		t.Errorf("the label does not say what to go and fix: %q", lbl)
	}

	// A monitor whose wake agent is gone keeps the relink wording, because
	// that IS the repair.
	MarkEventMonitorBroken(db, "craig", "dead", "wakes deleted agent \"X\"")
	m, _ = GetEventMonitor(db, "craig", "dead")
	if lbl := m.StopLabel(); !strings.Contains(lbl, "needs relink") {
		t.Errorf("a missing wake agent lost its repair: %q", lbl)
	}
}
