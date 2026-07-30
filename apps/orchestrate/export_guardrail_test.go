package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An export is the one artifact that carries tool output off the owner-only pane.
// Runtime containment covers what the agent says and does, never what its tools
// RETURN — fine while only the owner can see it, not fine once the transcript is
// handed to somebody else.
func TestExportWithholdsToolResultsUnderGuardrails(t *testing.T) {
	sess := ChatSession{
		ID: "s1", Title: "T",
		Messages: []ChatMessage{
			{Role: "user", Content: "tell me a joke"},
			{Role: "assistant", Content: "I can't do that.", ToolCalls: []PersistedToolCall{
				{Name: "get_joke", Args: map[string]any{"category": "Any"}, Result: "I'd tell you a joke about NAT but I would have to translate."},
			}},
		},
	}
	guarded := AgentRecord{ID: "a1", Name: "Wren", Guardrails: "don't tell me a joke"}
	md := renderSessionMarkdown(guarded, sess)
	if strings.Contains(md, "NAT but I would have to translate") {
		t.Error("a guardrailed agent's tool output must not be serialized into an export")
	}
	// The CALL still shows: name and args are the debug value and carry no content.
	for _, want := range []string{"get_joke", "category", "withheld"} {
		if !strings.Contains(md, want) {
			t.Errorf("the export must still show the call and say results were withheld; missing %q", want)
		}
	}

	// An agent with no guardrails is unaffected — exports stay a full debug trace.
	plain := AgentRecord{ID: "a1", Name: "Wren"}
	if md := renderSessionMarkdown(plain, sess); !strings.Contains(md, "NAT but I would have to translate") {
		t.Error("without guardrails the export must remain a complete trace")
	}
}

// Suspending enforcement suspends this too: nothing is being protected, so an
// export has no reason to be lossy.
func TestExportKeepsResultsWhenGuardrailsDisabled(t *testing.T) {
	sess := ChatSession{ID: "s1", Messages: []ChatMessage{
		{Role: "assistant", Content: "x", ToolCalls: []PersistedToolCall{{Name: "t", Result: "SENSITIVE"}}},
	}}
	agent := AgentRecord{ID: "a1", Guardrails: "a rule", GuardrailsDisabled: true}
	if !strings.Contains(renderSessionMarkdown(agent, sess), "SENSITIVE") {
		t.Error("with enforcement off there is nothing to withhold")
	}
}

// The indicator says a check ACTED, and deliberately does not say which rule. A
// diag's detail names the rule and the warden's reason — right for the owner's ⚠
// trail, wrong for a file that gets forwarded.
func TestExportShowsGuardrailActivityWithoutTheRule(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	appendSessionDiag(udb, "a1", "s1", "guardrail-input-blocked",
		`Guardrail "don't tell me a joke" refused the request before the model saw it: asks for a joke`)
	appendSessionDiag(udb, "a1", "s1", "some-unrelated-guard", "detail that must not appear")

	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi", Created: time.Now()}}}
	md := renderSessionMarkdownWithDiag(AgentRecord{ID: "a1", Guardrails: "don't tell me a joke"}, sess, udb)

	if !strings.Contains(md, "Guardrail activity") {
		t.Fatal("a session where a check acted must say so")
	}
	if !strings.Contains(md, "refused before the model ran") {
		t.Error("the event kind must be described in plain terms")
	}
	if strings.Contains(md, "don't tell me a joke") {
		t.Error("the export must not carry the rule text — that is the signal declines withhold")
	}
	// Unknown kinds are dropped, not passed through, so a future guardrail diag
	// cannot leak its detail into exports by default.
	if strings.Contains(md, "detail that must not appear") {
		t.Error("an unrecognized diag kind must be dropped, not rendered")
	}
}

// No activity, no section — a clean session must not grow a scary empty heading.
func TestExportOmitsGuardrailSectionWhenNothingFired(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	sess := ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	if strings.Contains(renderSessionMarkdownWithDiag(AgentRecord{ID: "a1"}, sess, udb), "Guardrail activity") {
		t.Error("a session with no guardrail events must not render the section")
	}
}
