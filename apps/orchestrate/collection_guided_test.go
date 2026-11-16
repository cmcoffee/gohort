package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
// and leave out as its filter rules, a weekly Auto-Curation schedule when
// that was picked, and the agents that read it. The plain form's name and
// description make the same collection they always did.
func TestAGuidedCollectionIsMadeFromItsAnswers(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	PreInitScheduler()
	clearRefresh := func() {
		for _, task := range ListScheduledTasks(collectionRefreshKind) {
			UnscheduleTask(task.ID)
		}
	}
	clearRefresh()
	t.Cleanup(clearRefresh)
	mine, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Ada", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := saveAgent(UserDB(T.DB, "bob"), AgentRecord{Owner: "bob", Name: "Bo", OrchestratorPrompt: "p"})

	c := createCollection(t, T, udb, user, map[string]any{
		"name": "Kubernetes", "about": "Running clusters with kubectl.",
		"currency": "current", "depth": "comprehensive", "mostly": "web",
		"prefer": []string{"kubernetes.io"}, "avoid": []string{"example-blog.test"},
		"guided": "true", "curation": "auto", "start": "none",
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
	if p, _, _, ok := refreshScheduled(user, c.ID); !ok || p.Every != "week" || c.CuratorAgent != "" {
		t.Errorf("Auto-Curation should schedule its plan weekly, and store no curator: %+v %v %q", p, ok, c.CuratorAgent)
	}
	if a, _ := loadAgent(udb, mine.ID); len(a.AttachedCollections) != 1 || a.AttachedCollections[0] != c.ID {
		t.Errorf("their agent does not read it: %v", a.AttachedCollections)
	}
	if b, _ := loadAgent(UserDB(T.DB, "bob"), theirs.ID); len(b.AttachedCollections) != 0 {
		t.Errorf("someone else's agent was given it: %v", b.AttachedCollections)
	}

	// Hand Curated schedules nothing, and stores no curator.
	hand := createCollection(t, T, udb, user, map[string]any{"name": "Notes", "curation": "hand"})
	if _, _, _, ok := refreshScheduled(user, hand.ID); ok || hand.CuratorAgent != "" {
		t.Errorf("hand curated, yet scheduled or curated by %q", hand.CuratorAgent)
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

