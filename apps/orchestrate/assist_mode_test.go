package orchestrate

// The assist window's two modes reach the model. The window sends "discuss"
// or "write" on every turn and the person can switch between them, so each
// endpoint has to pick its reply contract from the turn, not once.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAgentAssistPicksTheContractByMode(t *testing.T) {
	p := buildAssistSystemPrompt("orchestrator_prompt", "", "", "", AssistModeDiscuss, nil)
	if !strings.Contains(p, DiscussReplyContract) || strings.Contains(p, DraftReplyContract) {
		t.Error("discuss mode should carry the discuss contract and not the revise one")
	}
	for _, mode := range []string{"write", ""} {
		p := buildAssistSystemPrompt("orchestrator_prompt", "", "", "", mode, nil)
		if !strings.Contains(p, DraftReplyContract) || strings.Contains(p, DiscussReplyContract) {
			t.Errorf("mode %q should carry the revise contract", mode)
		}
	}
}

// A workflow step's assist conversation: its system prompt asks for the
// instructions alone, so without a reply contract of its own the new text
// came back as chat and the draft never changed. Now the fenced text is the
// new draft, and the mode picks the contract.
func TestMachineAssistConversationReturnsTheDraft(t *testing.T) {
	def := MachineDef{Name: "Review", Phases: []MachinePhase{{Name: "work", Prompt: "do the work"}}}
	run := func(mode string) (*FakeLLM, map[string]any) {
		fake := &FakeLLM{Turns: []FakeTurn{{Content: "Shortened it.\n```draft\nRead the log first.\n```"}}}
		T := &OrchestrateApp{AppCore: AppCore{LLM: fake}}
		body, _ := json.Marshal(map[string]any{
			"field": "prompt", "record": map[string]any{"name": "work"},
			"message": "shorter", "draft": "do the work", "mode": mode,
		})
		w := httptest.NewRecorder()
		T.handleMachineSuggest(w, httptest.NewRequest("POST", "/suggest", strings.NewReader(string(body))), nil, "u", def)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("mode %q: %d %s", mode, w.Code, w.Body.String())
		}
		return fake, out
	}
	fake, out := run("write")
	if out["value"] != "Read the log first." || out["reply"] != "Shortened it." {
		t.Errorf("the reply and the draft were not split: %v", out)
	}
	if sp := fake.Config(0).SystemPrompt; !strings.Contains(sp, DraftReplyContract) {
		t.Error("write mode should carry the revise contract")
	}
	fake, _ = run(AssistModeDiscuss)
	if sp := fake.Config(0).SystemPrompt; !strings.Contains(sp, DiscussReplyContract) || strings.Contains(sp, DraftReplyContract) {
		t.Error("discuss mode should carry the discuss contract and not the revise one")
	}
}
