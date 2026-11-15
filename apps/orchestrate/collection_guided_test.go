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
		"name": "Kubernetes", "about": "Running clusters with kubectl.",
		"currency": "current", "depth": "comprehensive", "mostly": "web",
		"prefer": []string{"kubernetes.io"}, "avoid": []string{"example-blog.test"},
		"guided": "true", "curation": "auto", "auto_curator": curator, "start": "none",
		"attach_to": []string{mine.ID, theirs.ID},
	})
	for _, want := range []string{"Running clusters with kubectl.", guidedCurrency["current"], guidedDepth["comprehensive"], guidedMostly["web"]} {
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
		t.Errorf("Auto-Curation did not put the curator in charge: %q", c.CuratorAgent)
	}
	if a, _ := loadAgent(udb, mine.ID); len(a.AttachedCollections) != 1 || a.AttachedCollections[0] != c.ID {
		t.Errorf("their agent does not read it: %v", a.AttachedCollections)
	}
	if b, _ := loadAgent(UserDB(T.DB, "bob"), theirs.ID); len(b.AttachedCollections) != 0 {
		t.Errorf("someone else's agent was given it: %v", b.AttachedCollections)
	}

	// Hand Curated, the curator the form carries is not put in charge; nor
	// is an Auto-Curation agent that is no agent at all. Curation by Agent
	// puts their agent in charge, and not someone else's.
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Notes", "curation": "hand", "auto_curator": curator}); c.CuratorAgent != "" {
		t.Errorf("hand curated, yet curated by %q", c.CuratorAgent)
	}
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Odd", "curation": "auto", "auto_curator": "nobody-at-all"}); c.CuratorAgent != "" {
		t.Errorf("a curator that is no agent was stored: %q", c.CuratorAgent)
	}
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Mine", "curation": "agent", "curator_agent": mine.ID}); c.CuratorAgent != mine.ID {
		t.Errorf("Curation by Agent: %q", c.CuratorAgent)
	}
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Theirs", "curation": "agent", "curator_agent": theirs.ID}); c.CuratorAgent != "" {
		t.Errorf("someone else's agent was made curator: %q", c.CuratorAgent)
	}
	// The description they settled on is kept, rather than their first words.
	if c := createCollection(t, T, udb, user, map[string]any{"name": "Drafted", "guided": "true", "about": "k8s stuff",
		"description": "The official Kubernetes documentation and kubectl reference.", "start": "none"}); c.Description != "The official Kubernetes documentation and kubectl reference." {
		t.Errorf("the settled description: %q", c.Description)
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

// The guided setup starts what its "To start with" says as it creates:
// Suggest sources when nothing was chosen, a plan, or nothing. The plain form
// starts nothing.
func TestTheGuidedSetupStartsWhatItWasAsked(t *testing.T) {
	guided := json.RawMessage(`"true"`)
	for _, tc := range []struct {
		g    guidedCollection
		want string
	}{
		{guidedCollection{Guided: guided}, "suggest"},
		{guidedCollection{Guided: guided, Start: "suggest"}, "suggest"},
		{guidedCollection{Guided: guided, Start: "plan"}, "plan"},
		{guidedCollection{Guided: guided, Start: "none"}, "none"},
		{guidedCollection{Guided: guided, Start: "anything else"}, "suggest"},
		{guidedCollection{}, ""},
		{guidedCollection{About: "topic", Start: "plan"}, ""},
	} {
		if got := tc.g.starts(); got != tc.want {
			t.Errorf("%+v: starts %q, want %q", tc.g, got, tc.want)
		}
	}
}

// The collection page sends how it is curated: Hand Curated clears the
// curator, Auto-Curation sets the agent it names only when that is the app's
// hidden curator, Curation by Agent sets one of the person's own agents and
// changes nothing until one is picked. A caller sending only curator_agent
// sets it as before. Reading it back gives the choice and, for Curation by
// Agent, which one.
func TestTheCurationChoiceIsStoredAsTheCurator(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	const curator = "app-test-steward-curator"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: curator, OwningApp: "Test", Name: "Curator", Prompt: "p", Hidden: true})
	mine, _ := saveAgent(udb, AgentRecord{Owner: user, Name: "Ada", OrchestratorPrompt: "p"})
	saveCollection(udb, Collection{ID: "col-c", Name: "C", Owner: user})
	save := func(body map[string]any) (Collection, int) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		r := asUser(httptest.NewRequest(http.MethodPost, "/api/collections/col-c/steward", strings.NewReader(string(raw))), user)
		r.SetPathValue("id", "col-c")
		r.Header.Set("Content-Type", "application/json")
		jsonapi.Post(T.DB, T.collectionStewardSave)(w, r)
		c, _ := loadCollection(udb, user, "col-c")
		return c, w.Code
	}
	read := func() map[string]any {
		r := asUser(httptest.NewRequest(http.MethodGet, "/api/collections/col-c/steward", nil), user)
		r.SetPathValue("id", "col-c")
		out, err := T.collectionSteward(&jsonapi.Call{W: httptest.NewRecorder(), R: r, User: user, UDB: udb})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if c, code := save(map[string]any{"curation": "auto", "auto_curator": curator}); code != 200 || c.CuratorAgent != curator {
		t.Fatalf("Auto-Curation: %d %q", code, c.CuratorAgent)
	}
	if got := read(); got["curation"] != "auto" || got["curator_agent"] != "" {
		t.Errorf("read back: %v", got)
	}
	if _, code := save(map[string]any{"curation": "auto", "auto_curator": mine.ID}); code == 200 {
		t.Error("Auto-Curation took one of the person's agents")
	}
	if c, _ := save(map[string]any{"curation": "agent", "curator_agent": ""}); c.CuratorAgent != curator {
		t.Errorf("Curation by Agent with none picked yet changed the curator: %q", c.CuratorAgent)
	}
	if c, code := save(map[string]any{"curation": "agent", "curator_agent": mine.ID}); code != 200 || c.CuratorAgent != mine.ID {
		t.Fatalf("Curation by Agent: %d %q", code, c.CuratorAgent)
	}
	if got := read(); got["curation"] != "agent" || got["curator_agent"] != mine.ID {
		t.Errorf("read back: %v", got)
	}
	if _, code := save(map[string]any{"curation": "agent", "curator_agent": "nobody"}); code == 200 {
		t.Error("an agent that is not theirs was taken")
	}
	if c, _ := save(map[string]any{"curation": "hand"}); c.CuratorAgent != "" {
		t.Errorf("Hand Curated left %q in charge", c.CuratorAgent)
	}
	if got := read(); got["curation"] != "hand" {
		t.Errorf("read back: %v", got)
	}
	if c, _ := save(map[string]any{"curator_agent": "Ada"}); c.CuratorAgent != mine.ID {
		t.Errorf("a caller naming the curator alone: %q", c.CuratorAgent)
	}
}
