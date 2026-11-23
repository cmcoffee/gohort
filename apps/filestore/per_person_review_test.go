package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A people folder that becomes a link after the setting is on (planted by an
// admin's hand, or by anything that could write at the store's top) is never
// followed by the cleanup sweep: it lists nothing, outside the store or in.
func TestTheSweepNeverFollowsAPeopleLink(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root, outside := t.TempDir(), t.TempDir()
	victim := aged(t, filepath.Join(outside, "someapp"), "OLD-DATA", 3)
	if _, err := SaveStore(app.DB, Store{Name: "Drop", Path: root, PerPerson: true, Ephemeral: true, LifetimeHours: 24}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "people")); err != nil {
		t.Skip(err)
	}
	for _, b := range FindExpiredBundles(context.Background(), app.DB) {
		t.Errorf("the sweep listed %s (%s) through a people link", b.Path, b.Folder)
	}
	reapExpired(context.Background(), app.DB, true)
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the sweep deleted %s, outside the store", victim)
	}
	st, _ := LoadStore(app.DB, "drop")
	if _, err := st.forPerson("alice"); err == nil {
		t.Error("a person's folder was made through a people link")
	}
}

// Turning the setting on over a people folder it did not make is refused,
// since each folder in it would go to whoever's name it matches; one it made
// (an earlier time it was on), or an empty one, is fine.
func TestPerPersonIsRefusedOverAPeopleFolderItDidNotMake(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "people", "alice"), 0o755)
	st, err := SaveStore(app.DB, Store{Name: "Old", Path: root, Members: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	st.PerPerson = true
	if _, err := SaveStore(app.DB, st); err == nil || !strings.Contains(err.Error(), "did not make") {
		t.Fatalf("turned on over someone else's people folder: %v", err)
	}
	os.RemoveAll(filepath.Join(root, "people"))
	os.WriteFile(filepath.Join(root, "people"), []byte("a file"), 0o644)
	if _, err := SaveStore(app.DB, st); err == nil {
		t.Error("turned on over a file called people")
	}
	os.Remove(filepath.Join(root, "people"))
	on, err := SaveStore(app.DB, st)
	if err != nil {
		t.Fatalf("turned on over nothing: %v", err)
	}
	if _, err := on.forPerson("alice"); err != nil {
		t.Fatal(err)
	}
	// Off, then on again: the people folder is the setting's own.
	on.PerPerson = false
	off, _ := SaveStore(app.DB, on)
	off.PerPerson = true
	if _, err := SaveStore(app.DB, off); err != nil {
		t.Errorf("turned back on over its own people folder: %v", err)
	}
}

// Two people pulling the same name from their own folders of a per-person
// source into a shared store are two jobs: neither joins, watches or stops the
// other's, and each copies what is in their own folder.
func TestPullsFromEachPersonsSourceFolderAreTheirOwnJobs(t *testing.T) {
	app, intake, intakeRoot := perPersonFixture(t)
	os.MkdirAll(filepath.Join(intakeRoot, "people", "bob", "CASE-A"), 0o755)
	os.WriteFile(filepath.Join(intakeRoot, "people", "bob", "CASE-A", "bob.log"), []byte("bob's"), 0o644)
	destRoot := t.TempDir()
	dest, err := SaveStore(app.DB, Store{Name: "Shared", Path: destRoot, PullFrom: intake.Slug, PullSubpath: ".",
		Members: []string{"alice", "bob"}, FullAccess: []string{"alice", "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, who := range []string{"alice", "bob"} {
		mine, _ := dest.forPerson(who)
		job, err := app.pullJobFor(mine, "CASE-A", who, false)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, job.key())
	}
	if keys[0] == keys[1] {
		t.Errorf("alice's and bob's pulls share the job %q", keys[0])
	}
	bob, _ := dest.forPerson("bob")
	job, _ := app.pullJobFor(bob, "CASE-A", "bob", false)
	if _, err := waitForPull(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(destRoot, "CASE-A", "bob.log")); string(b) != "bob's" {
		t.Error("bob's pull did not copy his own CASE-A")
	}
	if _, err := os.Stat(filepath.Join(destRoot, "CASE-A", "app.log")); err == nil {
		t.Error("bob's pull copied alice's files")
	}
}

// A per-person source reached as nobody offers nothing to pull, not its people
// folder, which would be everyone's at once.
func TestAPerPersonSourceReachedAsNobodyOffersNothing(t *testing.T) {
	app, intake, _ := perPersonFixture(t)
	dest, _ := SaveStore(app.DB, Store{Name: "Shared", Path: t.TempDir(), PullFrom: intake.Slug,
		Members: []string{"alice"}, FullAccess: []string{"alice"}})
	nobody, err := dest.forPerson("")
	if err != nil {
		t.Fatal(err)
	}
	if rows := app.pullItems(nobody); len(rows) != 0 {
		t.Errorf("offered with nobody named: %v", rows)
	}
	if _, err := app.pullJobFor(nobody, "people", "", false); err == nil {
		t.Error("a pull of people was set up with nobody named")
	}
}

// An owner's agent working for someone else (their use of the owner's app)
// reaches that person's folder through its tools, its mapped commands' folder
// values and its generic search, under the owner's access.
func TestAnOwnersAgentWorkingForSomeoneReachesTheirFolder(t *testing.T) {
	app, st, _ := perPersonFixture(t)
	st.Members, st.FullAccess = []string{"alice"}, []string{"alice"} // bob is not on the store
	SaveStore(app.DB, st)
	src := storeSource{app: app}
	sess := &ToolSession{Ctx: context.Background(), Username: "alice", StorePerson: "bob"}
	out, err := toolNamed(t, src.ItemToolsWithSession(sess, "alice", st.Slug), "search_"+st.Slug).Handler(context.Background(), map[string]any{"pattern": "ERROR"})
	if err != nil || !strings.Contains(out, "bob's license") || strings.Contains(out, "alice's disk") {
		t.Errorf("alice's agent working for bob searched: %q %v", out, err)
	}
	if p, err := app.resolveScope("alice", "bob", st.Slug, "CASE-B"); err != nil || !strings.Contains(p, filepath.Join("people", "bob")) {
		t.Errorf("a folder value for bob: %q %v", p, err)
	}
	if _, err := app.resolveScope("alice", "bob", st.Slug, "CASE-A"); err == nil {
		t.Error("a folder value for bob resolved in alice's folder")
	}
	got := src.Fetch(appstores.WithPerson(context.Background(), "bob"), "alice", st.Slug, "ERROR")
	if strings.Contains(got, "alice's disk") || !strings.Contains(got, "bob's license") {
		t.Errorf("the generic search for bob: %q", got)
	}
}

// A store's mapped command, run by an owner's agent working for someone else,
// takes its folder values in that person's folder, and refuses the owner's.
func TestAMappedCommandForSomeoneTakesFoldersFromTheirFolder(t *testing.T) {
	app, st, _ := perPersonFixture(t)
	prev := registeredFileStoreApp
	registeredFileStoreApp = app
	t.Cleanup(func() { registeredFileStoreApp = prev })
	st.Members, st.FullAccess = []string{"alice"}, []string{"alice"}
	SaveStore(app.DB, st)
	bin := filepath.Join(t.TempDir(), "where.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"in $1\"\n"), 0o755)
	SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "where", Command: bin})
	if _, err := SaveCommandTools(app.DB, st.Slug, "where", "Says where.", []TempToolAction{{Name: "show", Description: "Show.",
		CommandTemplate: bin + " {folder}", Params: map[string]ToolParam{"folder": {Type: "string", PathScope: "files:" + st.Slug}},
		Required: []string{"folder"}}}); err != nil {
		t.Fatal(err)
	}
	SetCommandApproved(app.DB, st.Slug, "where", true)
	var show AgentToolDef
	for _, td := range (storeSource{app: app}).ItemToolsWithSession(&ToolSession{Ctx: context.Background(), Username: "alice", StorePerson: "bob"}, "alice", st.Slug) {
		if strings.HasSuffix(td.Tool.Name, "_show") {
			show = td
		}
	}
	if show.Handler == nil {
		t.Fatal("no show tool")
	}
	out, err := show.Handler(context.Background(), map[string]any{"folder": "CASE-B"})
	if err != nil || !strings.Contains(out, filepath.Join("people", "bob", "CASE-B")) {
		t.Errorf("bob's folder value: %q %v", out, err)
	}
	if out, err := show.Handler(context.Background(), map[string]any{"folder": "CASE-A"}); err == nil && !strings.Contains(out, "no file or folder") {
		t.Errorf("a folder of alice's resolved for bob: %q", out)
	}
}
