package core

import (
	"strings"
	"testing"
)

// A router is told what each place it can send the conversation can do, and
// what this agent's own tools are. It chose from names alone, and "make a song
// about it" went to a step that could only write the lyrics back, past the
// one that held the music tool.
func TestARouterKnowsWhatEachStepCanDo(t *testing.T) {
	def := MachineDef{Name: "Humor", Start: "Router", Phases: []MachinePhase{
		{Name: "Router", Desc: "Decides where the message goes.", Prompt: "Route: {input}", Choices: []string{"Comedian", "Answer", "Summary"}},
		{Name: "Comedian", Desc: "Writes jokes.", Prompt: "Joke: {input}", Agent: "JokeBot", Next: "Answer"},
		{Name: "Summary", Desc: "Sums it up.", Prompt: "Sum: {input}", Reach: ReachNone, Next: "Answer"},
		{Name: "Answer", Desc: "Answers directly.", Prompt: "Answer.", Resident: true},
	}}
	router, _ := def.Phase("Router")
	v := PhaseVars{MachineTurn: MachineTurn{Input: "make a song about it", Tools: []string{"generate_music: Makes a song from a description."}}}
	got := def.PhaseInstructions(router, MachineState{}, v)
	for _, want := range []string{
		"- Comedian: Writes jokes. (hands the work to the JokeBot agent, which works with its own tools)",
		"- Answer: Answers directly. (can use this agent's tools)",
		"- Summary: Sums it up. (writes text only, no tools)",
		"generate_music: Makes a song from a description.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the router should see %q:\n%s", want, got)
		}
	}
	// With no step that uses the agent's own tools, the list is not shown.
	def.Phases[3] = MachinePhase{Name: "Answer", Desc: "Answers directly.", Prompt: "Answer.", Resident: true, Tools: []string{"web_search"}}
	got = def.PhaseInstructions(router, MachineState{}, v)
	if strings.Contains(got, "generate_music") || !strings.Contains(got, "(can use web_search)") {
		t.Errorf("a step naming its tools lists those, and the agent's list is left out:\n%s", got)
	}
}
