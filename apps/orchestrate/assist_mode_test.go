package orchestrate

// The assist window's two modes reach the model. The window sends "discuss"
// or "write" on every turn and the person can switch between them, so each
// endpoint has to pick its reply contract from the turn, not once.

import (
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
