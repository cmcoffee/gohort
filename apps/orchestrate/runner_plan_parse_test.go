package orchestrate

import "testing"

// A plan whose every step only replies is refused; a step that does work is
// not, however its brief happens to be worded.
func TestVacuousPlan(t *testing.T) {
	cases := []struct {
		name  string
		steps []PlanStep
		want  bool
	}{
		{"reply-only step", []PlanStep{{Title: "Respond to user", Intent: "respond directly"}}, true},
		{"brief names respond_directly", []PlanStep{{Title: "Wrap up", Intent: "close out", WorkerBrief: "Call respond_directly with the summary."}}, true},
		{"tools only respond_directly", []PlanStep{{Title: "Reply to the user", Tools: []string{"respond_directly"}}}, true},
		// Observed: a one-step probe refused on a phrase in its brief.
		{"tool step with a reply phrase in its brief", []PlanStep{{
			Title:       "Probe the endpoint the app will call",
			Intent:      "Find a working model and request shape.",
			Tools:       []string{"workspace"},
			WorkerBrief: "Try each model and report what works in your final response.",
		}}, false},
		{"no tools, reply phrase only in the brief", []PlanStep{{
			Title:       "Check the release notes",
			Intent:      "Find what changed in v2.",
			WorkerBrief: "Summarize it in your final response.",
		}}, false},
		{"one reply step after real work", []PlanStep{
			{Title: "Search the docs", Tools: []string{"web_search"}},
			{Title: "Reply to the user"},
		}, false},
	}
	for _, c := range cases {
		if got := looksLikeVacuousPlan(c.steps) != ""; got != c.want {
			t.Errorf("%s: vacuous=%v, want %v", c.name, got, c.want)
		}
	}
}
