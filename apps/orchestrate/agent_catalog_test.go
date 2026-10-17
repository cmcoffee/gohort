package orchestrate

// An agent attached as a Source can be read without running it: what it
// recorded, searched at once, before anyone asks it. Only the user's own
// agents offer it, and it never reads their conversations.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnAgentsCatalogIsReadWithoutRunningIt(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	lib, err := saveAgent(udb, AgentRecord{Name: "GitLab Librarian", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	ns := factsNamespace(lib.ID)
	StoreMemoryFact(udb, ns, "The billing service lives in the payments/billing repository, owned by the payments team.")
	svc, _ := UpsertGraphEntity(udb, ns, "thing", "billing service", nil, nil)
	repo, _ := UpsertGraphEntity(udb, ns, "thing", "payments/billing", nil, nil)
	LinkGraphEdge(udb, ns, svc.ID, "lives_in", repo.ID, "", false)

	tools := map[string]AgentToolDef{}
	for _, td := range (agentReferenceSource{app: app}).ItemTools(user, lib.ID) {
		tools[td.Tool.Name] = td
	}
	search, ok := tools["search_gitlab_librarian_memory"]
	if !ok {
		t.Fatalf("no search tool for an own agent: %v", tools)
	}
	if _, ok := tools["consult_gitlab_librarian"]; !ok {
		t.Error("consult went missing beside the catalogue tools")
	}
	for name, td := range tools {
		if name == "consult_gitlab_librarian" {
			continue
		}
		if len(td.Tool.Caps) != 1 || td.Tool.Caps[0] != CapRead {
			t.Errorf("%s is not read-only: %v", name, td.Tool.Caps)
		}
	}

	out, err := search.Handler(context.Background(), map[string]any{"query": "billing service"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "lives in → payments/billing") || !strings.Contains(out, "owned by the payments team") {
		t.Errorf("the search did not find what it recorded:\n%s", out)
	}
	if out, _ := search.Handler(context.Background(), map[string]any{"query": "kubernetes cluster"}); !strings.Contains(out, "nothing recorded") || !strings.Contains(out, "consult_gitlab_librarian") {
		t.Errorf("a miss should say so and point at consult: %s", out)
	}

	about := tools["recall_gitlab_librarian_about"]
	if out, _ := about.Handler(context.Background(), map[string]any{}); !strings.Contains(out, "billing service") || !strings.Contains(out, "payments/billing") {
		t.Errorf("with no name it should list what it has mapped: %s", out)
	}
	if out, _ := about.Handler(context.Background(), map[string]any{"name": "billing service"}); !strings.Contains(out, "payments/billing") || !strings.Contains(out, "Notes that name it") {
		t.Errorf("one thing, its links and its notes: %s", out)
	}

	// Someone else's agent, shared or not, stays consult-only.
	other, err := saveAgent(udb, AgentRecord{Name: "Borrowed", Owner: "bob", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.agentCatalogTools(user, other, "Borrowed", "consult_borrowed"); got != nil {
		t.Errorf("another user's agent offered its memory: %v", got)
	}
}
