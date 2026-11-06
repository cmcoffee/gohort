package orchestrate

// Builder's collections tool puts an agent in charge of a collection: one of
// the owner's own, resolved to the id the grant matches on, never a guess.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

func TestTheCollectionsToolNamesACurator(t *testing.T) {
	store := stewardStores(t)
	udb := store("alice")
	saveCollection(udb, Collection{ID: "col-a", Name: "Runbooks", Owner: "alice", AllowedUsers: []string{"carol"}, Contributors: []string{"carol"}})
	cur, err := saveAgent(udb, AgentRecord{Name: "Keeper", Owner: "alice", OrchestratorPrompt: "keeps", OwnedBy: ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Twin", "Twin"} {
		if _, err := saveAgent(udb, AgentRecord{Name: n, Owner: "alice", OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	gt := collectionsListTool().(*GroupedTool)
	update, _ := gt.Action("update")
	as := func(user string) *ToolSession { return &ToolSession{Username: user, DB: store(user)} }

	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Keeper"}, as("alice")); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != cur.ID {
		t.Errorf("the curator should be stored as the agent's id: %q", c.CuratorAgent)
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Twin"}, as("alice")); err == nil {
		t.Error("a name two agents answer to was taken")
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Nobody"}, as("alice")); err == nil || !strings.Contains(err.Error(), "no agent of yours") {
		t.Errorf("an unknown agent should be refused, saying so: %v", err)
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Keeper"}, as("carol")); err == nil {
		t.Error("a contributor put somebody in charge of the owner's collection")
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != cur.ID {
		t.Error("a refused change changed the curator")
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": ""}, as("alice")); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != "" {
		t.Errorf("an empty curator should clear it: %q", c.CuratorAgent)
	}
}

// An agent shared with the user is somebody else's: it cannot be put in
// charge of the user's collection.
func TestACuratorIsTheUsersOwnAgent(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Researcher", OrchestratorPrompt: "research",
		AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	_, err := collectionCuratorFor(&ToolSession{Username: "bob", DB: UserDB(orchestrateBaseDB, "bob")}, "a1")
	if err == nil || !strings.Contains(err.Error(), "one of your own agents") {
		t.Errorf("an agent shared with the user was taken as their curator: %v", err)
	}
}

// add_file puts a file from the workspace into the collection the agent keeps:
// a document under its file name, the same title again replacing it rather
// than piling up copies, and nothing outside the workspace or with no text.
func TestAFileInTheWorkspaceGoesIntoTheCollection(t *testing.T) {
	store := stewardStores(t)
	udb := store("user1")
	saveCollection(udb, Collection{ID: "col-a", Name: "Reports", Owner: "user1", CuratorAgent: "agent-7"})
	ws := t.TempDir()
	body := strings.Repeat("The quarterly report covers uptime, incidents and the change backlog. ", 6)
	if err := os.WriteFile(filepath.Join(ws, "report.md"), []byte("# Q3\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "short.txt"), []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	gt := collectionStewardTool([]Collection{{ID: "col-a", Name: "Reports", Owner: "user1", CuratorAgent: "agent-7"}}).(*GroupedTool)
	add, ok := gt.Action("add_file")
	if !ok {
		t.Fatal("the keeper of a collection cannot add a file to it")
	}
	sess := &ToolSession{Username: "user1", DB: udb, WorkspaceDir: ws}
	docs := func() map[string]int {
		out := map[string]int{}
		for _, ch := range ChunksWhere(VectorDB, func(x EmbeddedChunk) bool { return x.Source == collectionSource("col-a") }) {
			out[ch.ReportID]++
		}
		return out
	}

	got, err := add.Handler(map[string]any{"path": "report.md"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Added document \"report.md\"") {
		t.Errorf("added: %s", got)
	}
	first := docs()
	if len(first) != 1 || first["file-col-a-report-md"] == 0 {
		t.Fatalf("the file is not one document of the collection: %v", first)
	}
	got, err = add.Handler(map[string]any{"path": "report.md"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Replaced") || len(docs()) != 1 {
		t.Errorf("the same file again should replace it: %s, %v", got, docs())
	}
	if _, err := add.Handler(map[string]any{"path": "report.md", "title": "Q3 report"}, sess); err != nil {
		t.Fatal(err)
	}
	if len(docs()) != 2 {
		t.Errorf("a new title should be a second document: %v", docs())
	}

	for _, bad := range []string{"../report.md", filepath.Join(ws, "report.md"), "missing.pdf", "https://example.com/a.pdf", "short.txt"} {
		if _, err := add.Handler(map[string]any{"path": bad}, sess); err == nil {
			t.Errorf("%q was added", bad)
		}
	}
	if _, err := add.Handler(map[string]any{"path": "report.md"}, &ToolSession{Username: "user1", DB: udb}); err == nil {
		t.Error("a run with no workspace added a file")
	}
	if len(docs()) != 2 {
		t.Errorf("a refused file changed the collection: %v", docs())
	}
}

// A PDF goes in by page, so a citation can name the page, and a file a tool
// saved with no extension is still read as the PDF its bytes say it is.
func TestAPDFFromTheWorkspaceKeepsItsPages(t *testing.T) {
	stewardStores(t)
	prev := media.DocumentConverter
	t.Cleanup(func() { media.DocumentConverter = prev })
	var asked string
	media.DocumentConverter = func(_ context.Context, _ []byte, mime string) (string, error) {
		asked = mime
		return strings.Repeat("Page one covers the scope. ", 6) + "\f" + strings.Repeat("Page two covers the findings. ", 6), nil
	}
	c := Collection{ID: "col-p", Name: "Papers", Owner: "user1"}
	res, err := addFileToCollection(context.Background(), VectorDB, c, "Audit", UploadSource{Name: "download", Data: []byte("%PDF-1.7\n...")})
	if err != nil {
		t.Fatal(err)
	}
	if asked != "application/pdf" {
		t.Errorf("a PDF saved with no extension was read as %q", asked)
	}
	pages := map[string]bool{}
	for _, ch := range ChunksWhere(VectorDB, func(x EmbeddedChunk) bool { return x.ReportID == res.ID }) {
		pages[ch.Locator] = true
	}
	if !pages["page 1"] || !pages["page 2"] {
		t.Errorf("the PDF's pages were not kept: %v", pages)
	}
}
