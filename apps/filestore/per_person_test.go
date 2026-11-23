package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/snugforge/kvlite"
)

// perPersonFixture is a store that gives each person their own folder,
// reached by alice and bob with Full Access, with a file each in their own
// folder and one left at the top from before the switch.
func perPersonFixture(t *testing.T) (*FileStoreApp, Store, string) {
	t.Helper()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("OLD-CASE/app.log", "ERROR from before each person had a folder")
	st, err := SaveStore(app.DB, Store{Name: "Captures", Path: root, PerPerson: true,
		Members: []string{"alice", "bob"}, FullAccess: []string{"alice", "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	put("people/alice/CASE-A/app.log", "ERROR alice's disk full")
	put("people/bob/CASE-B/app.log", "ERROR bob's license expired")
	return app, st, root
}

func toolNamed(t *testing.T, defs []AgentToolDef, name string) AgentToolDef {
	t.Helper()
	for _, d := range defs {
		if d.Tool.Name == name {
			return d
		}
	}
	t.Fatalf("no tool %s", name)
	return AgentToolDef{}
}

// An agent's tools reach only the person's own folder: each sees their own
// case and not the other's, and neither sees what sits at the top of the store.
func TestEachPersonsAgentSeesOnlyTheirOwnFolder(t *testing.T) {
	app, st, _ := perPersonFixture(t)
	src := storeSource{app: app}
	ctx := context.Background()
	for _, tc := range []struct{ user, mine, theirs, myCase, theirCase string }{
		{"alice", "alice's disk full", "bob's license", "CASE-A", "CASE-B"},
		{"bob", "bob's license expired", "alice's disk", "CASE-B", "CASE-A"},
	} {
		tools := src.ItemTools(tc.user, st.Slug)
		out, err := toolNamed(t, tools, "search_"+st.Slug).Handler(ctx, map[string]any{"pattern": "ERROR"})
		if err != nil {
			t.Fatalf("%s: %v", tc.user, err)
		}
		if !strings.Contains(out, tc.mine) {
			t.Errorf("%s's search missed their own file: %s", tc.user, out)
		}
		if strings.Contains(out, tc.theirs) || strings.Contains(out, "before each person") {
			t.Errorf("%s's search reached past their own folder: %s", tc.user, out)
		}
		list, _ := toolNamed(t, tools, "list_"+st.Slug).Handler(ctx, map[string]any{})
		if strings.Contains(list, "OLD-CASE") || !strings.Contains(list, tc.myCase) || strings.Contains(list, tc.theirCase) {
			t.Errorf("%s's listing: %s", tc.user, list)
		}
	}
	// Naming the other person's folder by a path is refused, not followed.
	tools := src.ItemTools("alice", st.Slug)
	for _, within := range []string{"../bob/CASE-B", "../../OLD-CASE", "/people/bob"} {
		if out, err := toolNamed(t, tools, "read_"+st.Slug).Handler(ctx, map[string]any{"within": within, "file": "app.log"}); err == nil {
			t.Errorf("alice read %s: %s", within, out)
		}
	}
	// A path scope (a command's folder value) resolves in her folder too.
	if _, err := app.resolveScope("alice", "", st.Slug, "CASE-B"); err == nil {
		t.Error("alice's command value resolved to bob's folder")
	}
	if p, err := app.resolveScope("alice", "", st.Slug, "CASE-A"); err != nil || !strings.Contains(p, filepath.Join("people", "alice")) {
		t.Errorf("alice's own folder: %q %v", p, err)
	}
	// Someone reaching it for the first time gets an empty folder of their
	// own, made then.
	st.Members = append(st.Members, "carol")
	SaveStore(app.DB, st)
	if got := app.listScope("carol", st.Slug); len(got) != 1 || got[0] != "." {
		t.Errorf("carol's first look: %v, want only her own empty folder", got)
	}
}

// An app reaches the caller's folder: what is mounted is theirs, its commands
// run there, and a page that only says what an app reaches names no folder.
func TestAnAppReachesTheFolderOfThePersonUsingIt(t *testing.T) {
	app, st, root := perPersonFixture(t)
	bin := filepath.Join(t.TempDir(), "where.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"ran in $1\"\n"), 0o755)
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "where", Label: "Where", Command: bin}); err != nil {
		t.Fatal(err)
	}
	st.AppGrants = []AppGrant{{App: "alice/pcap", Write: true, Commands: true}}
	SaveStore(app.DB, st)
	host := storeHost{app}

	grants := host.GrantsFor(appstores.App{Owner: "alice", Slug: "pcap", Caller: "bob"})
	if len(grants) != 1 || !grants[0].PerPerson || grants[0].Path != filepath.Join(root, "people", "bob") {
		t.Fatalf("bob using alice's app: %+v", grants)
	}
	if g := host.GrantsFor(appstores.App{Owner: "alice", Slug: "pcap"}); len(g) != 1 || g[0].Path != "" {
		t.Errorf("with nobody named, no folder is handed over: %+v", g)
	}
	out, err := host.RunCommand(context.Background(), appstores.App{Owner: "alice", Slug: "pcap", Caller: "bob"}, st.Slug, "CASE-B", "where", "")
	if err != nil || !strings.Contains(out, filepath.Join("people", "bob", "CASE-B")) {
		t.Errorf("bob's command: %q %v", out, err)
	}
	if _, err := host.RunCommand(context.Background(), appstores.App{Owner: "alice", Slug: "pcap", Caller: "bob"}, st.Slug, "CASE-A", "where", ""); err == nil {
		t.Error("bob's run of alice's app reached alice's folder")
	}
	if _, err := host.RunCommand(context.Background(), appstores.App{Owner: "alice", Slug: "pcap"}, st.Slug, "CASE-A", "where", ""); err == nil {
		t.Error("a run for nobody reached a person's folder")
	}
}

// A pull between two per-person stores goes from the person's folder in one
// to theirs in the other, and two people pulling the same name are two jobs.
func TestAPullStaysWithinThePersonsOwnFolders(t *testing.T) {
	app, intake, intakeRoot := perPersonFixture(t)
	os.MkdirAll(filepath.Join(intakeRoot, "people", "bob", "CASE-A"), 0o755)
	os.WriteFile(filepath.Join(intakeRoot, "people", "bob", "CASE-A", "bob.log"), []byte("bob's own CASE-A"), 0o644)
	destRoot := t.TempDir()
	dest, err := SaveStore(app.DB, Store{Name: "Analysis", Path: destRoot, PerPerson: true, PullFrom: intake.Slug, PullSubpath: ".",
		Members: []string{"alice", "bob"}, FullAccess: []string{"alice", "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, who := range []string{"alice", "bob"} {
		mine, err := dest.forPerson(who)
		if err != nil {
			t.Fatal(err)
		}
		job, err := app.pullJobFor(mine, "CASE-A", who, false)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, job.key())
		if _, err := waitForPull(context.Background(), job); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
	}
	if keys[0] == keys[1] {
		t.Errorf("alice's and bob's pulls of the same name share a job: %v", keys)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "people", "alice", "CASE-A", "app.log")); err != nil {
		t.Error("alice's pull did not land in her folder")
	}
	if b, _ := os.ReadFile(filepath.Join(destRoot, "people", "bob", "CASE-A", "bob.log")); string(b) != "bob's own CASE-A" {
		t.Error("bob's pull did not bring his own CASE-A into his folder")
	}
	if _, err := os.Stat(filepath.Join(destRoot, "people", "bob", "CASE-A", "app.log")); err == nil {
		t.Error("bob's pull copied alice's files")
	}
}

// The sweep of a temporary per-person store takes each person's expired
// folders, never a person's folder or the people folder, and names them
// with whose they were.
func TestATemporaryPerPersonStoreExpiresEachPersonsFolders(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	if _, err := SaveStore(app.DB, Store{Name: "Drop", Path: root, PerPerson: true, Ephemeral: true, LifetimeHours: 24}); err != nil {
		t.Fatal(err)
	}
	old := aged(t, filepath.Join(root, "people", "alice"), "CASE-OLD", 3)
	fresh := aged(t, filepath.Join(root, "people", "bob"), "CASE-NEW", 0)
	for _, p := range []string{filepath.Join(root, "people", "alice"), filepath.Join(root, "people")} {
		when := time.Now().AddDate(0, 0, -3)
		os.Chtimes(p, when, when)
	}
	list := FindExpiredBundles(context.Background(), app.DB)
	if len(list) != 1 || list[0].Folder != "alice/CASE-OLD" || list[0].Path != old {
		t.Fatalf("expired: %+v", list)
	}
	ReapExpiredBundles(context.Background(), app.DB)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the expired folder stayed")
	}
	for _, kept := range []string{fresh, filepath.Join(root, "people", "alice"), filepath.Join(root, "people")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed", kept)
		}
	}
}

// A person's folder that is a link (people/mallory pointing at alice's) is
// refused rather than followed, though it stays inside the store.
func TestAPersonsFolderThatIsALinkIsNotUsed(t *testing.T) {
	app, st, root := perPersonFixture(t)
	if err := os.Symlink(filepath.Join(root, "people", "alice"), filepath.Join(root, "people", "mallory")); err != nil {
		t.Skip(err)
	}
	st.Members = append(st.Members, "mallory")
	SaveStore(app.DB, st)
	if _, err := st.forPerson("mallory"); err == nil {
		t.Fatal("mallory's folder, a link to alice's, was used")
	}
	if tools := (storeSource{app: app}).ItemTools("mallory", st.Slug); len(tools) != 0 {
		t.Errorf("mallory got tools on alice's folder: %d", len(tools))
	}
}

// Names become folder names that are never a path and never shared: a plain
// name as itself, anything else marked with a + no plain name holds.
func TestPersonFolderNames(t *testing.T) {
	if got := personFolder("Alice"); got != "alice" {
		t.Errorf("plain name: %q", got)
	}
	if got := personFolder("bob@example.com"); got != "bob@example.com" {
		t.Errorf("an address is plain: %q", got)
	}
	seen := map[string]string{}
	for _, name := range []string{"a/b", "a_b", "a b", "..", ".", ".hidden", "../alice", "a\\b"} {
		got := personFolder(name)
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." || strings.HasPrefix(got, ".") {
			t.Errorf("%q made the folder name %q", name, got)
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("%q and %q share the folder %q", prev, name, got)
		}
		seen[got] = name
	}
	if !strings.Contains(personFolder("a b"), "+") {
		t.Error("a name that is not plain is not marked")
	}
}

// A person's copy of a store is never saved over the store, the size cap is
// the whole store's, and evidence indexing is refused alongside.
func TestAPersonsCopyOfAStoreStaysACopy(t *testing.T) {
	app, st, root := perPersonFixture(t)
	mine, err := st.forPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStore(app.DB, mine); err == nil {
		t.Error("alice's copy was saved over the store")
	}
	if got, _ := LoadStore(app.DB, st.Slug); got.Path != root {
		t.Errorf("the store's folder is now %s", got.Path)
	}
	mine.wrote()
	if got, all := mine.usage(), folderBytes(root); got != all {
		t.Errorf("alice's copy measures %d bytes, the store holds %d", got, all)
	}
	st.Evidence = true
	if _, err := SaveStore(app.DB, st); err == nil {
		t.Error("a per-person store was saved indexing evidence")
	}
	if got := st.folderCountLabel(); got != "2 people" {
		t.Errorf("admin count: %q", got)
	}
}

// The Files routes a person uses directly: an upload lands in their folder,
// the folder picker lists only theirs, and a command on someone else's folder
// is refused.
func TestThePersonsOwnRoutesReachOnlyTheirFolder(t *testing.T) {
	app, st, root := perPersonFixture(t)
	bin := filepath.Join(t.TempDir(), "touch.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o755)
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "touch", Label: "Touch", Command: bin}); err != nil {
		t.Fatal(err)
	}
	h := routed(app)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "capture.log")
	fw.Write([]byte("bob's capture"))
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/filestore/api/upload?slug="+st.Slug+"&within=CASE-NEW", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h(w, asAdmin(t, r, "bob"))
	if w.Code != http.StatusOK {
		t.Fatalf("bob's upload: %d %s", w.Code, w.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(root, "people", "bob", "CASE-NEW", "capture.log")); string(b) != "bob's capture" {
		t.Error("bob's upload did not land in his folder")
	}

	r = httptest.NewRequest("GET", "/api/folders?slug="+st.Slug, nil)
	w = httptest.NewRecorder()
	h(w, asAdmin(t, r, "bob"))
	var rows []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	var names []string
	for _, row := range rows {
		names = append(names, row["name"].(string))
	}
	if strings.Join(names, ",") != "CASE-B,CASE-NEW" && strings.Join(names, ",") != "CASE-NEW,CASE-B" {
		t.Errorf("bob's folder picker: %v", names)
	}

	for within, ok := range map[string]bool{"CASE-B": true, "CASE-A": false, "../alice/CASE-A": false} {
		r = httptest.NewRequest("POST", "/api/commands/run?slug="+st.Slug+"&within="+within+"&command=touch", nil)
		w = httptest.NewRecorder()
		h(w, asAdmin(t, r, "bob"))
		if (w.Code == http.StatusOK) != ok {
			t.Errorf("bob running a command on %s: %d %s", within, w.Code, w.Body.String())
		}
	}
}
