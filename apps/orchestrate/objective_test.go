package orchestrate

import (
	"context"
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
