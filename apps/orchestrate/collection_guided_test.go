package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// createCollection posts body to the create endpoint as the test user and
// returns the collection it made, read back from the store.
func createCollection(t *testing.T, T *OrchestrateApp, udb Database, user string, body map[string]any) Collection {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	r := asUser(httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(string(raw))), user)
	r.Header.Set("Content-Type", "application/json")
	jsonapi.Post(T.DB, T.collectionCreate)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var made Collection
	json.Unmarshal(w.Body.Bytes(), &made)
	c, ok := loadCollection(udb, user, made.ID)
	if !ok {
		t.Fatalf("the created collection is not in the store: %s", w.Body.String())
	}
	return c
}

// The guided setup's answers become what a collection already has: their own
// words and what each answer says as its description, the sources to prefer
// and leave out as its filter rules, the curator when "Look after it for me"
// was picked, and the agents that read it. The plain form's name and
// description make the same collection they always did.
func TestAGuidedCollectionIsMadeFromItsAnswers(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	const curator = "app-test-guided-curator"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: curator, OwningApp: "Test", Name: "Curator", Prompt: "p", Hidden: true})
	mine, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Ada", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := saveAgent(UserDB(T.DB, "bob"), AgentRecord{Owner: "bob", Name: "Bo", OrchestratorPrompt: "p"})

	c := createCollection(t, T, udb, user, map[string]any{
		"name": "Kubernetes", "purpose": "reference", "about": "Running clusters with kubectl.",
		"currency": "current", "depth": "comprehensive", "mostly": "web",
		"prefer": []string{"kubernetes.io"}, "avoid": []string{"example-blog.test"},
		"fill": "curated", "curator_agent": curator,
		"attach_to": []string{mine.ID, theirs.ID},
	})
	for _, want := range []string{"Running clusters with kubectl.", guidedPurposes["reference"], guidedCurrency["current"], guidedDepth["comprehensive"], guidedMostly["web"]} {
		if !strings.Contains(c.Description, want) {
			t.Errorf("the description is missing %q: %q", want, c.Description)
		}
	}
	if !strings.HasPrefix(c.Description, "Running clusters") {
		t.Errorf("their own words should lead: %q", c.Description)
	}
	if c.FilterRules != "Prefer kubernetes.io\nSkip example-blog.test" {
		t.Errorf("filter rules: %q", c.FilterRules)
	}
	if c.CuratorAgent != curator {
		t.Errorf("Look after it for me did not put the curator in charge: %q", c.CuratorAgent)
	}
	if a, _ := loadAgent(udb, mine.ID); len(a.AttachedCollections) != 1 || a.AttachedCollections[0] != c.ID {
		t.Errorf("their agent does not read it: %v", a.AttachedCollections)
	}
	if b, _ := loadAgent(UserDB(T.DB, "bob"), theirs.ID); len(b.AttachedCollections) != 0 {
		t.Errorf("someone else's agent was given it: %v", b.AttachedCollections)
	}

	// Filled by hand, the curator the form carries is not put in charge; nor
	// is a curator that is no agent at all.
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Notes", "purpose": "own_files", "fill": "by_hand", "curator_agent": curator}); c.CuratorAgent != "" {
		t.Errorf("by hand, yet curated by %q", c.CuratorAgent)
	}
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Odd", "fill": "curated", "curator_agent": "nobody-at-all"}); c.CuratorAgent != "" {
		t.Errorf("a curator that is no agent was stored: %q", c.CuratorAgent)
	}
	// With no description and no words, the card's line describes it.
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Topic", "purpose": "topic"}); c.Description != guidedPurposes["topic"] {
		t.Errorf("a card alone: %q", c.Description)
	}

	plain := createCollection(t, T, udb, user, map[string]any{"name": "Plain", "description": "  Just this.  "})
	if plain.Description != "Just this." || plain.FilterRules != "" || plain.CuratorAgent != "" {
		t.Errorf("the plain form's collection changed shape: %+v", plain)
	}
}

// The curator's run is told the owner's rules, so preferred and excluded
// sources steer Curate now as they steer Auto-fill.
func TestTheCurateBriefCarriesTheRules(t *testing.T) {
	b := curateBrief(Collection{Name: "K", FilterRules: "Prefer kubernetes.io\nSkip example-blog.test"}, 0, 10)
	if !strings.Contains(b, "Prefer kubernetes.io\nSkip example-blog.test") {
		t.Errorf("the brief has no rules:\n%s", b)
	}
	if strings.Contains(curateBrief(Collection{Name: "K"}, 0, 10), "rules") {
		t.Error("a collection with no rules is told about rules")
	}
}
