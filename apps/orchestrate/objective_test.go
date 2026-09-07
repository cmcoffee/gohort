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

	if got := objectiveAttemptsBlock(base); got != "" {
		t.Errorf("the first attempt has nothing to report, got %q", got)
	}
	notObjective := base
	notObjective.Until = ""
	notObjective.Attempts = []objectiveAttempt{{At: "2026-09-04T16:00:00Z", Reason: "whatever"}}
	if got := objectiveAttemptsBlock(notObjective); got != "" {
		t.Errorf("an ordinary recurring task gets no objective block, got %q", got)
	}

	p := base
	p.MaxAttempts = 5
	p.Attempts = []objectiveAttempt{
		{At: "2026-09-04T16:00:00Z", Reason: "drafted but never published (create_post returned 401)"},
		{At: "2026-09-05T16:00:00Z", Reason: "published, but the thread reply carried the title, not the URL"},
		{At: "2026-09-06T16:00:00Z", Reason: "URL posted to the wrong thread"},
	}
	block := objectiveAttemptsBlock(p)
	for _, want := range []string{
		"the post is published", // the goal, restated for this fire
		"Attempts so far: 3 of 5",
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

	// Without a bound the count still shows, because "how many times has this
	// already failed" is the point; there is just no denominator.
	noBound := p
	noBound.MaxAttempts = 0
	if b := objectiveAttemptsBlock(noBound); !strings.Contains(b, "Attempts so far: 3.") {
		t.Errorf("an unbounded objective still reports its attempt count:\n%s", b)
	}
}

// TestNoteObjectiveAttemptBoundsAndIsolates: the history rides a payload that
// was COPIED from the firing one, so appending must not reach back into the
// original's backing array, and it must stay a note rather than growing into a
// transcript that rides every fire's prompt.
func TestNoteObjectiveAttemptBoundsAndIsolates(t *testing.T) {
	p := orchUpdatePayload{Attempts: []objectiveAttempt{{Reason: "first"}}}
	armed := p // the pre-armed successor: same backing array
	noteObjectiveAttempt(&armed, false, "second")
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
		noteObjectiveAttempt(&full, false, fmt.Sprintf("reason %d", i))
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
