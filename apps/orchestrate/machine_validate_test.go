// Authoring a machine through the tool used to be write-blind: the actions were
// create, update, update_phase, list, get, repair, delete, and the only route to
// the findings was to save and read what came back. That is expensive in one
// specific way — `update` REPLACES the whole phase list, so a single bad field
// refuses the entire save, including the phases that were fine.
package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func validateTurn(t *testing.T) (*chatTurn, Database, string) {
	t.Helper()
	udb, user := preflightFixture(t)
	return &chatTurn{app: &OrchestrateApp{}, ctx: context.Background(), user: user, udb: udb}, udb, user
}

func phaseArg(m map[string]any) any { return m }

// The refusal an author used to discover by losing a save.
func TestValidateReportsWhatWouldRefuseTheSaveWithoutWriting(t *testing.T) {
	turn, udb, user := validateTurn(t)
	out, err := turn.machineValidate(map[string]any{
		"name": "draft",
		// A transient step that hands off nowhere: refused by Validate.
		"phases": []any{
			phaseArg(map[string]any{"name": "look", "prompt": "look around"}),
			phaseArg(map[string]any{"name": "answer", "prompt": "reply", "resident": true}),
		},
	})
	if err != nil {
		t.Fatalf("validate must never fail; its whole job is to SAY what is wrong: %v", err)
	}
	if !strings.Contains(out, "WOULD BE REFUSED") {
		t.Errorf("a machine Validate rejects must be reported as such: %q", out)
	}
	if !strings.Contains(out, "NOTHING WAS WRITTEN") {
		t.Errorf("validate must say plainly that it wrote nothing: %q", out)
	}
	if len(ListMachineDefs(udb, user)) != 0 {
		t.Error("validate stored a machine; it must never write")
	}
}

// A runnable candidate says so, and says where a session would start.
func TestValidatePassesARunnableCandidate(t *testing.T) {
	turn, udb, user := validateTurn(t)
	out, err := turn.machineValidate(map[string]any{
		"name": "draft",
		"phases": []any{
			phaseArg(map[string]any{"name": "look", "prompt": "look around", "next": "answer"}),
			phaseArg(map[string]any{"name": "answer", "prompt": "reply", "resident": true}),
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "WOULD SAVE") || !strings.Contains(out, "would start in look") {
		t.Errorf("a runnable candidate should be cleared and say where it starts: %q", out)
	}
	if strings.Contains(out, "WOULD BE REFUSED") {
		t.Errorf("a runnable machine must not be reported as refused: %q", out)
	}
	if len(ListMachineDefs(udb, user)) != 0 {
		t.Error("validate stored a machine; it must never write")
	}
}

// The word the framework prints back at an author has to pass the check it is
// most likely to be pasted into.
func TestValidateAcceptsReachAll(t *testing.T) {
	turn, _, _ := validateTurn(t)
	out, _ := turn.machineValidate(map[string]any{
		"name": "draft",
		"phases": []any{
			phaseArg(map[string]any{"name": "look", "prompt": "go", "reach": "all", "next": "answer"}),
			phaseArg(map[string]any{"name": "answer", "prompt": "reply", "resident": true}),
		},
	})
	if strings.Contains(out, "WOULD BE REFUSED") {
		t.Errorf("reach \"all\" is the sayable spelling of the default and must validate: %q", out)
	}
}

// The call worth having after something ELSE changed: a bare {name} re-checks
// what is stored, because an agent's allowlist is rewritten far from here and
// the phases naming those tools are only wrong afterwards.
func TestValidateBareNameChecksWhatIsStored(t *testing.T) {
	turn, udb, user := validateTurn(t)
	withBundleSource(t)
	SaveMachineDef(udb, MachineDef{ID: "m1", Name: "stored", Owner: user, Start: "look",
		Phases: []MachinePhase{
			{Name: "look", Prompt: "go", Next: "answer", Tools: []string{"repo_search"}},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})
	ag := AgentRecord{ID: "a1", Name: "Wren", Owner: user, Machine: "m1", OrchestratorPrompt: "you help"}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("agent: %v", err)
	}

	out, err := turn.machineValidate(map[string]any{"name": "stored"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "Checked the stored") {
		t.Errorf("a bare name checks the stored definition and should say so: %q", out)
	}
	if !strings.Contains(out, "repo_search") || !strings.Contains(out, "Wren") {
		t.Errorf("the attached agent cannot reach what step look names; that is the finding: %q", out)
	}
}

// "Would this work on that agent" has to be askable before the answer costs
// anything — attachMachineToAgents saves, which a validate must never do.
func TestValidateChecksNamedAgentsWithoutAttachingThem(t *testing.T) {
	turn, udb, user := validateTurn(t)
	ag := AgentRecord{ID: "a1", Name: "Wren", Owner: user, OrchestratorPrompt: "you help"}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("agent: %v", err)
	}
	out, err := turn.machineValidate(map[string]any{
		"name":             "draft",
		"attach_to_agents": []any{"Wren"},
		"phases": []any{
			phaseArg(map[string]any{"name": "look", "prompt": "go", "next": "answer",
				"tools": []any{"repo_search"}}),
			phaseArg(map[string]any{"name": "answer", "prompt": "reply", "resident": true}),
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "Wren") || !strings.Contains(out, "repo_search") {
		t.Errorf("the preflight against a named agent should be reported: %q", out)
	}
	// The agent must be untouched: a preflight that attaches is not a preflight.
	again, ok := findAgentByNameOrID(udb, user, "a1")
	if !ok {
		t.Fatal("agent vanished")
	}
	if again.Machine != "" {
		t.Errorf("validate attached the machine to %q; it must only ask", again.Name)
	}
}

// An unparseable phase list is a verdict, not a tool error. An action that
// exists to report what is wrong must not fail instead of reporting it.
func TestValidateReportsRatherThanFailing(t *testing.T) {
	turn, _, _ := validateTurn(t)
	out, err := turn.machineValidate(map[string]any{"name": "draft", "phases": []any{"not an object"}})
	if err != nil {
		t.Errorf("validate returned an error instead of a verdict: %v", err)
	}
	if !strings.Contains(out, "NOT VALID") || !strings.Contains(out, "NOTHING WAS WRITTEN") {
		t.Errorf("a bad phase list should come back as a verdict: %q", out)
	}
}

// A machine with nothing wrong says so out loud. "No news" and "checked and
// clean" are the same output otherwise, and an author cannot tell whether the
// check ran.
func TestValidateSaysWhenThereIsNothingToReport(t *testing.T) {
	turn, _, _ := validateTurn(t)
	out, _ := turn.machineValidate(map[string]any{
		"name": "draft",
		"phases": []any{
			phaseArg(map[string]any{"name": "answer", "prompt": "reply", "resident": true}),
		},
	})
	if !strings.Contains(out, "Nothing else to report") {
		t.Errorf("a clean machine must say it is clean: %q", out)
	}
}
