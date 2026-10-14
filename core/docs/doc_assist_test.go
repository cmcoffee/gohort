package docs

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/textutil"
)

// The document assist takes the window's mode on every turn: talking it
// through gets the discuss contract, anything else the revise one.
func TestDocAssistPromptFollowsTheMode(t *testing.T) {
	p := BuildDocAssistPrompt("Runbook", "", "", "", textutil.AssistModeDiscuss)
	if !strings.Contains(p, textutil.DiscussReplyContract) || strings.Contains(p, textutil.DraftReplyContract) {
		t.Error("discuss mode should carry the discuss contract and not the revise one")
	}
	p = BuildDocAssistPrompt("Runbook", "Setup", "some text", "", "write")
	if !strings.Contains(p, textutil.DraftReplyContract) || strings.Contains(p, textutil.DiscussReplyContract) {
		t.Error("write mode should carry the revise contract")
	}
}
