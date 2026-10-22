package core

import (
	"strings"
	"testing"
)

// A step that hands its work to an agent, a pipeline or a child machine
// passes on the task, not its routing block and output contract: those are
// for the call that reads the answer into the step's fields.
func TestADelegatedStepPassesOnTheTaskAlone(t *testing.T) {
	def := MachineDef{Name: "m", Phases: []MachinePhase{
		{Name: "triage", Prompt: "Work out what the person needs.", Agent: "helper",
			Output: []PipelineField{{Name: "need", Type: "string"}, {Name: "next", Type: "string", Enum: []string{"answer", "escalate"}}}, NextFrom: "next"},
		{Name: "answer", Desc: "reply", Resident: true},
		{Name: "escalate", Desc: "hand it on", Resident: true},
	}}
	ph := def.Phases[0]
	prompt := def.phasePrompt(ph, nil, PhaseVars{}) + renderOutputContract(ph.ModelOutput())
	task, shape := SplitDelegatedStepPrompt(prompt)
	if !strings.Contains(task, "Work out what the person needs.") {
		t.Fatalf("the task lost its prompt: %q", task)
	}
	for _, leaked := range []string{"Reply with a single JSON object", "Where this goes next"} {
		if strings.Contains(task, leaked) {
			t.Errorf("the delegate's task carries %q:\n%s", leaked, task)
		}
		if !strings.Contains(shape, leaked) {
			t.Errorf("the shaping half lost %q:\n%s", leaked, shape)
		}
	}
	if !strings.HasPrefix(prompt, task) || !strings.HasSuffix(prompt, shape) {
		t.Error("the split lost text")
	}
	if got, rest := SplitDelegatedStepPrompt("just a task"); got != "just a task" || rest != "" {
		t.Errorf("a prompt with neither: %q / %q", got, rest)
	}
}
