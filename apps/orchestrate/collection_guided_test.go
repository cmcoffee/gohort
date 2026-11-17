package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		{guidedCollection{Guided: guided, Kind: "custom"}, "suggest"},
		{guidedCollection{Guided: guided, Kind: "custom", Start: "suggest"}, "suggest"},
		{guidedCollection{Guided: guided, Kind: "custom", Start: "plan"}, "plan"},
		{guidedCollection{Guided: guided, Kind: "custom", Start: "none"}, "none"},
		{guidedCollection{Guided: guided, Kind: "custom", Start: "anything else"}, "suggest"},
		// No kind picked is the usual kind; with no web search in a test,
		// My own files, which starts nothing.
		{guidedCollection{Guided: guided, Start: "plan"}, "none"},
		// The older curation answer picks no kind, and its Start holds.
		{guidedCollection{Guided: guided, Curation: "hand", Start: "plan"}, "plan"},
		{guidedCollection{}, ""},
		{guidedCollection{About: "topic", Start: "plan"}, ""},
	} {
		if got := tc.g.starts(); got != tc.want {
			t.Errorf("%+v: starts %q, want %q", tc.g, got, tc.want)
		}
	}
}

// A kind sets the real settings a collection has: its schedule, how much a
// run adds, a rule and what starts. Reference is the standard practice; a
// topic is checked weekly for what is new; files are never searched for;
// Set it up yourself takes its own answers, a count sent as a string too.
func TestAKindSetsWhatACollectionStartsWith(t *testing.T) {
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
	every := func(c Collection) string {
		if p, _, _, ok := refreshScheduled(user, c.ID); ok {
			return p.Every
		}
		return ""
	}
	for i, tc := range []struct {
		body        map[string]any
		every       string
		docs        int
		rule, start string
	}{
		{map[string]any{"kind": "reference", "prefer": []string{"docs.example.com"}}, "month", 20, "Prefer the official documentation", "plan"},
		{map[string]any{"kind": "topic"}, "week", 10, "Skip pages more than a year old", "plan"},
		{map[string]any{"kind": "files"}, "", 0, "", "none"},
		{map[string]any{"kind": "custom", "refresh": "month", "docs_per_run": "15", "start": "suggest"}, "month", 15, "", "suggest"},
		{map[string]any{"kind": "custom", "refresh": "never", "docs_per_run": 500}, "", 100, "", "suggest"},
		// None picked: the usual kind, My own files without web search.
		{map[string]any{"kind": ""}, "", 0, "", "none"},
	} {
		body := map[string]any{"name": "Kind " + strconv.Itoa(i), "guided": "true", "start": "none"}
		for k, v := range tc.body {
			body[k] = v
		}
		c := createCollection(t, T, udb, user, body)
		if got := every(c); got != tc.every {
			t.Errorf("%v: scheduled %q, want %q", tc.body, got, tc.every)
		}
		if c.DocsPerRun != tc.docs {
			t.Errorf("%v: %d documents a run, want %d", tc.body, c.DocsPerRun, tc.docs)
		}
		if tc.rule != "" && !strings.HasPrefix(c.FilterRules, tc.rule) {
			t.Errorf("%v: rules %q, want to start %q", tc.body, c.FilterRules, tc.rule)
		}
		if tc.rule == "" && strings.Contains(c.FilterRules, "Skip pages") {
			t.Errorf("%v: rules %q", tc.body, c.FilterRules)
		}
		g := guidedCollection{Guided: json.RawMessage(`"true"`), Kind: tc.body["kind"].(string)}
		if s, ok := tc.body["start"].(string); ok {
			g.Start = s
		}
		if got := g.starts(); got != tc.start {
			t.Errorf("%v: starts %q, want %q", tc.body, got, tc.start)
		}
	}
	// The rule the kind adds comes before the sources the person named.
	ref := createCollection(t, T, udb, user, map[string]any{"name": "Ref two", "guided": "true", "start": "none", "kind": "reference", "prefer": []string{"docs.example.com"}})
	if !strings.HasSuffix(ref.FilterRules, "\nPrefer docs.example.com") {
		t.Errorf("rules: %q", ref.FilterRules)
	}
}
