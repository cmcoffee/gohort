package filestore

// A store indexing evidence: each folder indexed on its own, the folder never
// changed, the index following the folder, and the agent's tools reading it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// evidenceFixture is an app with one evidence store holding two case folders,
// and an in-memory bundle store behind the index.
func evidenceFixture(t *testing.T) (*FileStoreApp, Store) {
	t.Helper()
	prev := BundleFilesDB
	BundleFilesDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { BundleFilesDB = prev })
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	writeCase(t, root, "case-1", "scheduler.log", "2026-03-14T02:00:00Z ERROR scheduler stalled\n2026-03-14T02:00:09Z INFO retrying\n")
	writeCase(t, root, "case-2", "web.log", "2026-04-01T10:00:00Z WARN slow request\n")
	st, err := SaveStore(app.DB, Store{Name: "Cases", Path: root, Evidence: true})
	if err != nil {
		t.Fatal(err)
	}
	return app, st
}

func writeCase(t *testing.T, root, folder, file, body string) {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func evidenceTool(t *testing.T, app *FileStoreApp, st Store, name string) AgentToolDef {
	t.Helper()
	for _, td := range (storeSource{app: app}).ItemToolsWithSession(&ToolSession{Ctx: context.Background(), Username: "alice"}, "alice", st.Slug) {
		if td.Tool.Name == name {
			return td
		}
	}
	t.Fatalf("no tool %s", name)
	return AgentToolDef{}
}

func TestAnAgentReadsAFolderFromItsEvidenceIndex(t *testing.T) {
	app, st := evidenceFixture(t)
	before, _, _ := folderPrint(filepath.Join(st.Path, "case-1"))

	out, err := evidenceTool(t, app, st, "summarize_"+st.Slug).Handler(context.Background(), map[string]any{"folder": "case-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Evidence index of case-1") || !strings.Contains(out, "scheduler.log") {
		t.Errorf("the summary should come from case-1's index: %s", out)
	}
	if strings.Contains(out, "web.log") {
		t.Error("another folder's file showed up: two cases in one store must not mix")
	}
	if after, _, _ := folderPrint(filepath.Join(st.Path, "case-1")); after != before {
		t.Error("indexing changed the folder")
	}

	out, err = evidenceTool(t, app, st, "timeline_"+st.Slug).Handler(context.Background(), map[string]any{"folder": "case-1", "since": "2026-03-14 01:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scheduler stalled") || !strings.Contains(out, "retrying") {
		t.Errorf("the timeline should merge case-1's lines: %s", out)
	}

	for _, bad := range []string{"../case-2", "case-1/sub", ".incoming-x", "missing"} {
		if _, err := evidenceTool(t, app, st, "summarize_"+st.Slug).Handler(context.Background(), map[string]any{"folder": bad}); err == nil {
			t.Errorf("folder %q should be refused", bad)
		}
	}
}

// The index follows the folder: changed, it is indexed again when asked;
// deleted, the sweep wipes it; the switch off, every index goes.
func TestTheIndexFollowsTheFolder(t *testing.T) {
	app, st := evidenceFixture(t)
	dir := filepath.Join(st.Path, "case-1")
	sweepEvidence(context.Background(), app.DB)
	if _, current := evidenceCurrent(app.DB, st.Slug, "case-1", dir); !current {
		t.Fatal("the sweep should have indexed case-1")
	}
	if _, current := evidenceCurrent(app.DB, st.Slug, "case-2", filepath.Join(st.Path, "case-2")); !current {
		t.Fatal("the sweep should have indexed case-2")
	}

	writeCase(t, st.Path, "case-1", "late.log", "2026-03-14T03:00:00Z ERROR late arrival\n")
	if _, current := evidenceCurrent(app.DB, st.Slug, "case-1", dir); current {
		t.Fatal("a new file should make the index stale")
	}
	out, err := evidenceTool(t, app, st, "summarize_"+st.Slug).Handler(context.Background(), map[string]any{"folder": "case-1"})
	if err != nil || !strings.Contains(out, "late.log") {
		t.Errorf("asking about a changed folder should index it again first: %v %s", err, out)
	}

	os.RemoveAll(filepath.Join(st.Path, "case-2"))
	sweepEvidence(context.Background(), app.DB)
	if _, ok := evidenceStateOf(app.DB, st.Slug, "case-2"); ok || evidenceBundle(st.Slug, "case-2").FileCount() != 0 {
		t.Error("a deleted folder's index should be wiped")
	}

	if n := wipeStoreEvidence(app.DB, st.Slug); n != 1 || evidenceBundle(st.Slug, "case-1").FileCount() != 0 {
		t.Errorf("turning it off should wipe every index of the store, wiped %d", n)
	}
}

func TestAStoreNotIndexingEvidenceHasNoEvidenceTools(t *testing.T) {
	app, st := evidenceFixture(t)
	st.Evidence = false
	if _, err := SaveStore(app.DB, st); err != nil {
		t.Fatal(err)
	}
	for _, td := range (storeSource{app: app}).ItemToolsWithSession(&ToolSession{Ctx: context.Background(), Username: "alice"}, "alice", st.Slug) {
		if strings.HasPrefix(td.Tool.Name, "summarize_") || strings.HasPrefix(td.Tool.Name, "timeline_") {
			t.Errorf("%s offered with evidence off", td.Tool.Name)
		}
	}
	if got := storeToolsInForce(st); len(got) != len(storeToolNames(st.Slug)) {
		t.Errorf("the admin table lists %v", got)
	}
	noteChanged(app.DB, st, "case-1")
	if _, ok := evidenceStateOf(app.DB, st.Slug, "case-1"); ok {
		t.Error("an arrival in a store not indexing evidence indexed it")
	}
}
