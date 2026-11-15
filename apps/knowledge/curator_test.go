package knowledge

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// Knowledge's curating agent is registered hidden, so it is in no picker and
// nothing hands it work, and it can hand nothing on; it reads and finds pages,
// and adds them only through the collection tool being a curator brings.
func TestTheCuratingAgentIsHidden(t *testing.T) {
	spec, ok := appagents.AppAgentByID(curatorAgentID)
	if !ok {
		t.Fatal("the curating agent is not registered")
	}
	if !spec.Hidden || spec.EffectiveDispatchMode() != appagents.DispatchNone {
		t.Errorf("the curating agent must be hidden and dispatch nowhere: %+v", spec)
	}
	if !spec.DisableExplicit {
		t.Error("the curating agent stores facts, which would carry one collection into its runs for another")
	}
	if !strings.Contains(spec.Prompt, "Never remove a document") {
		t.Error("the curating agent's prompt no longer holds removals for the owner")
	}
	if strings.Contains(spec.Prompt, "\u2014") {
		t.Error("an em-dash in the curating agent's prompt")
	}
}

// The collection page's Curation offers Auto-Curation and Hand Curated at
// either end, with Curation by Agent between them only when there are agents
// to pick, carries the agent Auto-Curation means, and has Curate now below
// it, refreshed when the choice is saved.
func TestCurationOffersAutoCuratedAndCurateNow(t *testing.T) {
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
	if blob, _ := json.Marshal(s.Body); strings.Contains(string(blob), `"label":"Curation by Agent"`) {
		t.Error("Curation by Agent is offered to someone with no agents to pick")
	}
	blob, _ := json.Marshal(s.Body)
	out := string(blob)
	for _, want := range []string{
		`"options":[{"value":"auto","label":"Auto-Curation"`,
		`{"value":"hand","label":"Hand Curated"`,
		`{"field":"auto_curator","type":"hidden","default":"` + curatorAgentID + `"}`,
		`{"field":"refresh","label":"Refresh","type":"select"`,
		`"show_when":"curation:auto|agent"`,
		`{"value":"week","label":"Every week"`,
		`"source":"/orchestrate/api/collections/col-1/curate"`,
		`"progress_source":"/orchestrate/api/collections/col-1/curate/progress"`,
		`"cancel_to":"/orchestrate/api/collections/col-1/curate/stop"`,
		`"invalidate":["/orchestrate/api/collections/col-1/curate"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Curation is missing %s:\n%s", want, out)
		}
	}
}
