package filestore

// Store actions, exercised against a stand-in binary that behaves the way
// a real one does: folder alone prints something, folder plus input does
// the work.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func fakeCommand(t *testing.T, dir string) (bin, argvLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in is POSIX-only")
	}
	argvLog = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, "fake_action")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvLog + "\n" +
		"if [ $# -lt 2 ]; then echo 'CHALLENGE ab12-cd34'; else echo 'processed 3 file(s)'; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvLog
}

func actionFixture(t *testing.T, twoPhase bool) (*FileStoreApp, Store, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bundle-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, argvLog := fakeCommand(t, t.TempDir())
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Bundles", Path: root, AllowUploads: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStoreCommand(app.DB, StoreCommand{
		Slug: st.Slug, Name: "decrypt", Command: bin, TwoPhase: twoPhase, InputLabel: "Response key",
	}); err != nil {
		t.Fatal(err)
	}
	return app, st, argvLog, bin
}

func asAdmin(t *testing.T, r *http.Request, user string) *http.Request {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:"+user, AuthUser{Username: user})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
	return r
}

func TestTwoPhaseActionAsksThenRuns(t *testing.T) {
	app, st, argvLog, _ := actionFixture(t, true)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/commands/run?slug="+st.Slug+"&within=bundle-1&command=decrypt", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleCommand(w, asAdmin(t, r, "alice"))
		return w
	}

	w := post("")
	if w.Code != 200 {
		t.Fatalf("phase one: %d %s", w.Code, w.Body.String())
	}
	var ph1 struct {
		Challenge  string `json:"challenge"`
		InputLabel string `json:"input_label"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ph1)
	if !strings.Contains(ph1.Challenge, "CHALLENGE ab12-cd34") {
		t.Errorf("challenge not passed through verbatim: %q", ph1.Challenge)
	}
	// The UI needs to know what to ask for, and the action says.
	if ph1.InputLabel != "Response key" {
		t.Errorf("input label missing: %q", ph1.InputLabel)
	}

	w = post(`{"input":"resp o$nse;rm -rf /"}`)
	if w.Code != 200 {
		t.Fatalf("phase two: %d %s", w.Code, w.Body.String())
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 1 argument then 2, got %v", lines)
	}
	// No shell: the nasty input arrives INTACT, as one argument.
	if lines[2] != "resp o$nse;rm -rf /" {
		t.Errorf("input was split or mangled: %q", lines[2])
	}
	if !strings.HasSuffix(lines[0], "bundle-1") || !strings.HasSuffix(lines[1], "bundle-1") {
		t.Errorf("both phases should target the resolved folder: %v", lines[:2])
	}
}

// A one-phase action must not stop to ask for something it does not want.
func TestOnePhaseActionJustRuns(t *testing.T) {
	app, st, _, _ := actionFixture(t, false)
	r := httptest.NewRequest("POST", "/api/commands/run?slug="+st.Slug+"&within=bundle-1&command=decrypt", nil)
	w := httptest.NewRecorder()
	app.handleCommand(w, asAdmin(t, r, "alice"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if _, asked := out["challenge"]; asked {
		t.Error("a one-phase action returned a challenge — it should just run")
	}
	if _, ran := out["output"]; !ran {
		t.Errorf("expected output, got %v", out)
	}
}

func TestActionRefusals(t *testing.T) {
	app, st, _, bin := actionFixture(t, true)

	call := func(slug, within, action string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/commands/run?slug="+slug+"&within="+within+"&command="+action, nil)
		w := httptest.NewRecorder()
		app.handleCommand(w, asAdmin(t, r, "bob"))
		return w
	}
	if w := call(st.Slug, "../..", "decrypt"); w.Code == 200 {
		t.Error("traversal was accepted")
	}
	// An unknown action names what IS registered rather than just refusing.
	w := call(st.Slug, "bundle-1", "nope")
	if w.Code != 404 || !strings.Contains(w.Body.String(), "decrypt") {
		t.Errorf("refusal should list what exists: %d %s", w.Code, w.Body.String())
	}
	// Read-only store: a command rewrites the folder, so it takes the write grant.
	ro, _ := SaveStore(app.DB, Store{Name: "ReadOnly", Path: t.TempDir()})
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: ro.Slug, Name: "decrypt", Command: bin}); err != nil {
		t.Fatal(err)
	}
	if w := call(ro.Slug, "", "decrypt"); w.Code != 403 {
		t.Errorf("expected 403 without the write grant, got %d", w.Code)
	}
}

// Registration is admin-only and validated: this names a binary the
// server will execute.
func TestActionRegistrationRules(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, _ := SaveStore(app.DB, Store{Name: "Bundles", Path: t.TempDir()})

	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "x", Command: "diag_decrypt"}); err == nil {
		t.Error("a relative command should be refused — it resolves against whatever directory the server is in")
	}
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: "nosuch", Name: "x", Command: "/bin/true"}); err == nil {
		t.Error("an action on a store that does not exist should be refused")
	}
	saved, err := SaveStoreCommand(app.DB, StoreCommand{Slug: st.Slug, Name: "Decrypt Bundle", Command: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "decrypt_bundle" {
		t.Errorf("name should slug to a stable handle, got %q", saved.Name)
	}
	if saved.Label == "" {
		t.Error("a blank label should default to something readable rather than an empty button")
	}
}

// A page has to know which buttons a store offers, and the answer is a
// name and a label — the same thing the user is about to click. Reading
// the list is therefore not an admin act; registering one is.
func TestActionListIsReadableByAnAssignedUser(t *testing.T) {
	app, st, _, bin := actionFixture(t, true)
	// A second store the user cannot reach, with its own action.
	other, _ := SaveStore(app.DB, Store{Name: "Private", Path: t.TempDir(), AllowedUsers: []string{"someone_else"}})
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: other.Slug, Name: "secret", Command: bin}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/commands", nil)
	w := httptest.NewRecorder()
	app.handleCommands(w, asAdmin(t, r, "alice"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, row := range rows {
		names = append(names, row["name"].(string))
	}
	if len(names) != 1 || names[0] != "decrypt" {
		t.Fatalf("expected only the reachable store's action, got %v", names)
	}
	// Filtering to one store is what a page actually asks for.
	r = httptest.NewRequest("GET", "/api/commands?slug="+st.Slug, nil)
	w = httptest.NewRecorder()
	app.handleCommands(w, asAdmin(t, r, "alice"))
	rows = nil
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 {
		t.Errorf("slug filter returned %d rows", len(rows))
	}
	// The label the button needs, and whether it will ask for something.
	if rows[0]["label"] == "" || rows[0]["phases"] != "Response key" {
		t.Errorf("row should carry the button label and the input prompt: %+v", rows[0])
	}
}

func TestFolderListing(t *testing.T) {
	app, st, _, _ := actionFixture(t, false)
	r := httptest.NewRequest("GET", "/api/folders?slug="+st.Slug, nil)
	w := httptest.NewRecorder()
	app.handleFolders(w, asAdmin(t, r, "alice"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["name"] != "bundle-1" {
		t.Fatalf("expected the one bundle folder, got %+v", rows)
	}
	// A store the caller cannot reach answers the same way a missing one
	// does — its existence is not a fact they are owed.
	priv, _ := SaveStore(app.DB, Store{Name: "Private", Path: t.TempDir(), AllowedUsers: []string{"nobody"}})
	r = httptest.NewRequest("GET", "/api/folders?slug="+priv.Slug, nil)
	w = httptest.NewRecorder()
	app.handleFolders(w, asAdmin(t, r, "alice"))
	if w.Code != 404 {
		t.Errorf("expected 404 for an unreachable store, got %d", w.Code)
	}
}

// A rename that leaves data behind is a deletion with extra steps: the page
// would come up with no commands and no error, and an operator would rebuild by
// hand what was already registered.
func TestMigrateStoreCommandsMovesTheOldTable(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	old := StoreCommand{Slug: "bundles", Name: "decrypt", Label: "Decrypt bundle", Command: "/opt/bin/dec"}
	db.Set(legacyCommandsTable, commandKey(old.Slug, old.Name), old)

	if moved := MigrateStoreCommands(db); moved != 1 {
		t.Fatalf("moved %d, want the one registered command", moved)
	}
	got, ok := LoadStoreCommand(db, "bundles", "decrypt")
	if !ok || got.Command != "/opt/bin/dec" {
		t.Fatalf("the command must be readable at its new name: %+v ok=%v", got, ok)
	}
	if len(db.Keys(legacyCommandsTable)) != 0 {
		t.Error("the old row must be gone, or the next boot moves it again forever")
	}

	// Idempotent: every boot after the first is a no-op.
	if moved := MigrateStoreCommands(db); moved != 0 {
		t.Errorf("a second run must move nothing, moved %d", moved)
	}
	if _, ok := LoadStoreCommand(db, "bundles", "decrypt"); !ok {
		t.Error("and must not disturb what it already moved")
	}
}

// If the process died mid-migration the record exists in both places. The new
// one is authoritative — it may have been edited since — and the old copy is
// dropped rather than written over it.
func TestMigrateStoreCommandsKeepsTheNewerRecord(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	key := commandKey("bundles", "decrypt")
	db.Set(legacyCommandsTable, key, StoreCommand{Slug: "bundles", Name: "decrypt", Command: "/old/path"})
	db.Set(commandsTable, key, StoreCommand{Slug: "bundles", Name: "decrypt", Command: "/edited/path"})

	MigrateStoreCommands(db)
	got, _ := LoadStoreCommand(db, "bundles", "decrypt")
	if got.Command != "/edited/path" {
		t.Errorf("the migration must not overwrite a record already at the new name; got %q", got.Command)
	}
	if len(db.Keys(legacyCommandsTable)) != 0 {
		t.Error("the stale duplicate should still be cleared")
	}
}

// A store whose folders arrive encrypted or packed reads as EMPTY to an agent:
// it searches, finds nothing, and reports nothing found — when the truth is
// that a registered command has to be run on the folder first and nobody has
// clicked it. Naming the commands turns that dead end into an instruction.
func TestDescribeStoreCommandsTellsTheAgentWhatToAskFor(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	st := Store{Slug: "bundles", Name: "Support bundles"}

	empty := describeStoreCommands(db, st)
	if !strings.Contains(empty, "No commands are registered") {
		t.Errorf("with none registered it must say so plainly: %q", empty)
	}
	// "Nothing is set up to transform it" and "a step was skipped" are opposite
	// conclusions, and the agent has to be able to tell them apart.
	if !strings.Contains(empty, "nothing is set up to transform it") {
		t.Error("an empty list must rule the explanation OUT, not leave the agent guessing that something is missing")
	}

	// Written directly: SaveStoreCommand insists the store exists on disk, and
	// this test is about the SENTENCE it produces, not about store validation.
	put := func(c StoreCommand) { db.Set(commandsTable, commandKey(c.Slug, c.Name), c) }
	put(StoreCommand{Slug: "bundles", Name: "decrypt", Label: "Decrypt bundle",
		Command: "/opt/bin/dec", Help: "Run before reading a freshly uploaded bundle."})
	put(StoreCommand{Slug: "bundles", Name: "unseal", Label: "Unseal archive",
		Command: "/opt/bin/unseal", TwoPhase: true, InputLabel: "Response key"})
	// Another store's command must not leak into this one's answer.
	put(StoreCommand{Slug: "other", Name: "redact", Command: "/opt/bin/red"})

	got := describeStoreCommands(db, st)
	for _, want := range []string{"Decrypt bundle", "decrypt", "Run before reading", "Unseal archive"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from:\n%s", want, got)
		}
	}
	if strings.Contains(got, "redact") {
		t.Error("commands belong to one store; another store's must not appear")
	}
	// The two-phase one cannot be driven from here at all — its answer is looked
	// up outside gohort. Saying so is the difference between the agent asking a
	// person and the agent trying.
	if !strings.Contains(got, "Response key") || !strings.Contains(got, "cannot be run unattended") {
		t.Errorf("a two-phase command must name the input AND say a person is required:\n%s", got)
	}
	// It must never read as something the agent can call.
	if !strings.Contains(got, "never by you") {
		t.Errorf("the answer must be explicit that running one is a person's click:\n%s", got)
	}
}
