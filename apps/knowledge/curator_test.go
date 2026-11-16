package knowledge

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// Auto-Curation is the collection's plan run on a schedule now, not a hidden
// agent of Knowledge's own: nothing registers one.
func TestThereIsNoHiddenCuratingAgent(t *testing.T) {
	if _, ok := appagents.AppAgentByID("app-knowledge-curator"); ok {
		t.Error("the retired curating agent is still registered")
	}
}

// The collection page's Curation is the choice of who runs the plan (Auto-
// Curation and Hand Curated at either end, Curation by Agent between them
// only when there are agents to pick), its schedule, the plan itself to draft
// and edit (search terms and rules, each list naming what it adds), and Run
// now, each refreshing what it changes.
func TestCurationIsThePlanAndWhenItRuns(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks"})
	s, ok := app.stewardSection("alice", "col-1")
	if !ok {
		t.Fatal("the owner has no Curation section")
	}
	if s.Title != "Curation" {
		t.Errorf("section title %q", s.Title)
	}
	blob, _ := json.Marshal(s.Body)
	out := string(blob)
	if strings.Contains(out, `"label":"Curation by Agent"`) {
		t.Error("Curation by Agent is offered to someone with no agents to pick")
	}
	for _, want := range []string{
		`"options":[{"value":"auto","label":"Auto-Curation"`,
		`{"value":"hand","label":"Hand Curated"`,
		`{"field":"refresh","label":"Schedule","type":"select"`,
		`"show_when":"curation:auto|agent"`,
		`{"value":"week","label":"Every week"`,
		`"source":"/orchestrate/api/collections/col-1/plan/draft"`,
		`"progress_source":"/orchestrate/api/collections/col-1/plan/draft/progress"`,
		`"source":"/orchestrate/api/collections/col-1/plan"`,
		`"field":"search_terms","label":"Search terms","type":"rules"`,
		`"add_label":"+ Add search terms"`,
		`"placeholder":"No search terms yet`,
		`"field":"filter_rules","label":"Rules","type":"rules"`,
		`"add_label":"+ Add rule"`,
		`"field":"docs_per_run"`,
		`"source":"/orchestrate/api/collections/col-1/curate"`,
		`"button_text":"Run now"`,
		`"progress_source":"/orchestrate/api/collections/col-1/curate/progress"`,
		`"cancel_to":"/orchestrate/api/collections/col-1/curate/stop"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Curation is missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "auto_curator") || strings.Contains(out, "Curate now") {
		t.Error("Curation still names the retired curating agent or Curate now")
	}
}
