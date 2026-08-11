package orchestrate

import (
	"strings"
	"testing"
)

// A turn where the orchestrator neither planned nor asked gets a placeholder
// step so the pipeline still produces output. Rendering it as a plan is pure
// ceremony: the user sees "Plan: 1. Respond directly" on a turn that decided a
// plan was not needed, which was the first thing in the transcript.
func TestExportOmitsTheSyntheticPlan(t *testing.T) {
	sess := ChatSession{
		ID: "s1",
		Messages: []ChatMessage{
			{Role: "user", Content: "build me an agent"},
			{Role: "assistant", Content: "What should this agent do?"},
		},
		Plans: []PlanSnapshot{{
			RoundIndex: 0,
			Synthetic:  true,
			Steps: []PlanStep{{
				ID: 1, Title: "Respond directly", Status: StepPending,
				Output: "I've asked what the agent should do.",
			}},
		}},
	}
	out := renderSessionMarkdown(AgentRecord{Name: "Builder"}, sess)
	if strings.Contains(out, "**Plan:**") {
		t.Errorf("a synthetic plan must not render:\n%s", out)
	}
	if strings.Contains(out, "Respond directly") {
		t.Errorf("the placeholder step leaked into the transcript:\n%s", out)
	}
	// The actual reply still has to be there.
	if !strings.Contains(out, "What should this agent do?") {
		t.Errorf("the assistant's message went missing:\n%s", out)
	}
}

// A real plan still renders — this must not silence genuine multi-step work.
func TestExportStillRendersARealPlan(t *testing.T) {
	sess := ChatSession{
		ID: "s1",
		Messages: []ChatMessage{
			{Role: "user", Content: "research this"},
			{Role: "assistant", Content: "Done."},
		},
		Plans: []PlanSnapshot{{
			RoundIndex: 0,
			Steps: []PlanStep{
				{ID: 1, Title: "Search the docs", Intent: "find the API shape", Status: StepDone},
				{ID: 2, Title: "Summarize", Intent: "write it up", Status: StepDone},
			},
		}},
	}
	out := renderSessionMarkdown(AgentRecord{Name: "Research"}, sess)
	if !strings.Contains(out, "**Plan:**") || !strings.Contains(out, "Search the docs") {
		t.Errorf("a real plan must still render:\n%s", out)
	}
}
