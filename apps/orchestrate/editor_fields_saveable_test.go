package orchestrate

// A control the editor renders but PATCH refuses is worse than a missing one:
// it looks set, saves nothing, and says nothing. enable_notes shipped the other
// way round for a while — settable only by Builder's authoring tools, with the
// Memory pane telling owners to "enable them in the agent editor", which had no
// such control.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

var editorFieldRE = regexp.MustCompile(`ui\.FormField\{Field: "([a-z0-9_]+)"`)

var bareFieldRE = regexp.MustCompile(`\{Field: "([a-z0-9_]+)"`)

// formFieldLists returns the body of every []ui.FormField{...} literal in src,
// matched by brace depth, so a table's columns (also {Field: ...}) are not
// read as form fields.
func formFieldLists(src string) []string {
	var out []string
	const open = "[]ui.FormField{"
	for i := strings.Index(src, open); i >= 0; {
		start := i + len(open)
		depth, j := 1, start
		for ; j < len(src) && depth > 0; j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		out = append(out, src[start:j])
		next := strings.Index(src[j:], open)
		if next < 0 {
			break
		}
		i = j + next
	}
	return out
}

// Every field the agent editor renders must be one PATCH will accept.
func TestEveryEditorFieldIsSaveable(t *testing.T) {
	src, err := os.ReadFile("page_agent.go")
	if err != nil {
		t.Fatal(err)
	}
	// Fields the editor shows that PATCH refuses BY DESIGN.
	//
	// Two reasons, kept apart because they are different promises. A protected
	// field has its own endpoint so a partial save cannot reach it. A
	// full-record field rides the POST path, which decodes the whole
	// AgentRecord and never consults this allowlist — "machine" is one, and
	// treating it as missing was this test's own first false positive.
	savedAnotherWay := map[string]bool{
		"guardrails": true, "guardrails_disabled": true, "locked": true,
		"id": true, "owner": true, "created": true,
		"machine": true,
		// One choice over lead_model, lead_plans, lead_review and
		// consult_lead: PATCH translates it into those (applyLeadUse) before
		// the allowlist is consulted, so each of them has to be on it
		// (TestTheLeadChoiceSavesThroughPATCH).
		"lead_use": true,
		// The same over think and effort (applyReasoning,
		// TestTheReasoningChoiceSavesThroughPATCH).
		"reasoning": true,
	}
	// Two spellings: ui.FormField{Field: ...} on its own, and a bare
	// {Field: ...} inside a []ui.FormField{...} list. Reading only the first
	// missed run_priority, which shipped unsaveable: every change to it was
	// refused with a 400.
	names := editorFieldRE.FindAllStringSubmatch(string(src), -1)
	for _, list := range formFieldLists(string(src)) {
		names = append(names, bareFieldRE.FindAllStringSubmatch(list, -1)...)
	}
	var missing []string
	seen := map[string]bool{}
	for _, m := range names {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if savedAnotherWay[name] || patchAgentFields[name] {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Errorf("the editor renders %s, which PATCH will refuse — the control would save nothing. "+
			"Add to patchAgentFields, or to this test's savedAnotherWay list if it rides the full-record POST.",
			strings.Join(missing, ", "))
	}
}

// Agent-wide working notes are retired: nothing a person or Builder edits turns
// them on, and an agent that had them keeps its text as a fact.
func TestAgentNotesAreRetired(t *testing.T) {
	if patchAgentFields["enable_notes"] {
		t.Error("PATCH still takes enable_notes")
	}
	for file, needle := range map[string]string{"page_agent.go": `Field: "enable_notes"`, "agent_crud_tools.go": `"enable_notes"`} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), needle) {
			t.Errorf("%s still offers %s", file, needle)
		}
	}

	_, udb, _ := newTestOrchestrate(t)
	rec, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Wren", OrchestratorPrompt: "help", EnableNotes: true})
	if err != nil {
		t.Fatal(err)
	}
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "Project X: the deploy script is scripts/ship.sh")
	retireOneAgentNotes(udb, rec)
	got, _ := loadAgent(udb, rec.ID)
	if got.EnableNotes {
		t.Error("notes are still on")
	}
	found := false
	for _, f := range ListMemoryFacts(udb, factsNamespace(rec.ID)) {
		if strings.Contains(f.Note, "scripts/ship.sh") {
			found = true
		}
	}
	if !found {
		t.Error("the agent's notes were not kept as a fact")
	}
}

// Model priority saves from the editor, and only to a level that exists.
// Observed: every change to it on a new agent failed with a PATCH 400.
func TestRunPrioritySavesThroughPatch(t *testing.T) {
	T, udb, _ := newTestOrchestrate(t)
	rec, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Wren", OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string) int {
		w := httptest.NewRecorder()
		T.patchAgent(w, httptest.NewRequest(http.MethodPatch, "/api/agents/a1", strings.NewReader(body)), udb, "alice", rec.ID)
		return w.Code
	}
	if code := send(`{"run_priority":"lowest"}`); code != http.StatusOK {
		t.Fatalf("PATCH run_priority=lowest: %d", code)
	}
	if got, _ := loadAgent(udb, rec.ID); got.RunPriority != "lowest" {
		t.Fatalf("not saved: %q", got.RunPriority)
	}
	if code := send(`{"run_priority":"whenever"}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown level was accepted: %d", code)
	}
	if code := send(`{"run_priority":""}`); code != http.StatusOK {
		t.Fatalf("clearing it failed: %d", code)
	}
	if got, _ := loadAgent(udb, rec.ID); got.RunPriority != "" {
		t.Fatalf("not cleared: %q", got.RunPriority)
	}
}
