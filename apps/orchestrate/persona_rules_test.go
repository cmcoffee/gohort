package orchestrate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/gohort/core/ui"
)

// The persona no longer offers a Rules slot: the agent's Rules are the one
// place for them.
func TestThePersonaOutlineHasNoRulesSlot(t *testing.T) {
	for _, s := range orchestratorPromptSections {
		if strings.EqualFold(s.Title, "Rules") {
			t.Fatal("the persona outline still offers a Rules slot")
		}
	}
	if strings.Contains(fieldOutline("orchestrator_prompt"), "## Rules") {
		t.Error("Builder is still told to write a Rules section in the persona")
	}
}

// A persona's Rules section is found at any heading level, runs to the next
// heading at its level, keeps a deeper heading and its list, and loses the
// top-level bullets that made it a markdown list.
func TestAPersonasRulesSectionIsReadAsRules(t *testing.T) {
	for _, c := range []struct {
		name, in, rules, rest string
		ok                    bool
	}{
		{"none", "## Role & voice\nYou help.", "", "", false},
		{"empty", "## Role & voice\nYou help.\n\n## Rules\n\n## Output format\nShort.", "", "", false},
		{"between sections",
			"## Role & voice\nYou help.\n\n## Rules\n- Cite a source.\n2. Ask before deleting.\n\n## Output format\nShort.",
			"Cite a source.\nAsk before deleting.", "## Role & voice\nYou help.\n\n## Output format\nShort.", true},
		{"a deeper heading stays in it",
			"# Rules:\n- Be brief.\n## Investigating:\n1. Check the logs.\n# Output\nShort.",
			"Be brief.\n## Investigating:\nCheck the logs.", "# Output\nShort.", true},
		{"a nested list keeps its marks",
			"## RULES\n- Investigating:\n  1. Check the logs.\n  2. Ask first.",
			"Investigating:\n  1. Check the logs.\n  2. Ask first.", "", true},
	} {
		rules, rest, ok := personaRules(c.in)
		if ok != c.ok || (ok && (rules != c.rules || rest != c.rest)) {
			t.Errorf("%s:\n rules %q\n rest  %q\n ok %v", c.name, rules, rest, ok)
		}
	}
	// The nested one reads back as one rule with its steps.
	rules, _, _ := personaRules("## Rules\n- Investigating:\n  1. Check the logs.\n  2. Ask first.")
	if got := ruleItems(rules); len(got) != 1 || len(got[0]) != 3 {
		t.Errorf("a heading and its steps should be one rule: %q", got)
	}
}

// Moving puts the section after the Rules the agent has, takes it out of the
// persona, keeps the persona as it was in the history, and is refused when it
// would leave a heading with nothing under it.
func TestMovingThePersonasRulesIntoRules(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	persona := "## Role & voice\nYou help.\n\n## Rules\n- Cite a source.\n- Never delete without asking.\n\n## Output format\nShort."
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: persona, Rules: "Be brief."}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/agents/a1/persona-rules", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	routed(app)(w, as(t, r, "u"))
	if w.Code >= 300 || !strings.Contains(w.Body.String(), "Moved 2 rules") {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	a, _ := loadAgent(udb, "a1")
	if a.Rules != "Be brief.\n\nCite a source.\nNever delete without asking." {
		t.Errorf("rules after the move: %q", a.Rules)
	}
	if strings.Contains(a.OrchestratorPrompt, "Rules") || !strings.Contains(a.OrchestratorPrompt, "## Output format") {
		t.Errorf("persona after the move: %q", a.OrchestratorPrompt)
	}
	var prior AgentRecord
	if !revisions.Load(udb, revisions.KindAgent, "a1", "", &prior) || prior.OrchestratorPrompt != persona {
		t.Errorf("the persona as it was is not in the history: %q", prior.OrchestratorPrompt)
	}
	// Nothing left to move.
	w = httptest.NewRecorder()
	routed(app)(w, as(t, httptest.NewRequest(http.MethodPost, "/api/agents/a1/persona-rules", bytes.NewBufferString("{}")), "u"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a second move: %d", w.Code)
	}

	saveAgent(udb, AgentRecord{ID: "a2", Name: "Y", Owner: "u", OrchestratorPrompt: "## Rules\nEscalation:\nPage on-call."})
	b, _ := loadAgent(udb, "a2")
	if _, _, err := movePersonaRules(udb, b); err == nil {
		t.Error("a move that leaves a heading with nothing under it went through")
	}
}

// The Edit page offers the move after the persona, and only when there is
// something to move.
func TestTheEditPageOffersTheMoveOnlyWhenThereIsOne(t *testing.T) {
	_, udb := guardrailsPersistApp(t)
	saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "## Rules\n- Cite a source."})
	saveAgent(udb, AgentRecord{ID: "a2", Name: "Y", Owner: "u", OrchestratorPrompt: "You help."})
	sections := func(id string) []ui.Section {
		ed := &agentEditor{udb: udb, id: id, sections: []ui.Section{{Title: "Identity"}, {Title: "Persona"}, {Title: "Budgets"}}}
		ed.personaRulesSection()
		return ed.sections
	}
	if s := sections("a1"); len(s) != 4 || s[2].Title != "Rules in the persona" {
		t.Errorf("the move should be offered right after the persona: %+v", s)
	}
	if s := sections("a2"); len(s) != 3 {
		t.Errorf("a persona with no Rules section was offered a move: %+v", s)
	}
}
