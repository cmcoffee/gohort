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
