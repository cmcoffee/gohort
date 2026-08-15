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
	if _, err := SaveStoreAction(app.DB, StoreAction{
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
		r := httptest.NewRequest("POST", "/api/action?slug="+st.Slug+"&within=bundle-1&action=decrypt", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleAction(w, asAdmin(t, r, "alice"))
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
	r := httptest.NewRequest("POST", "/api/action?slug="+st.Slug+"&within=bundle-1&action=decrypt", nil)
	w := httptest.NewRecorder()
	app.handleAction(w, asAdmin(t, r, "alice"))
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
		r := httptest.NewRequest("POST", "/api/action?slug="+slug+"&within="+within+"&action="+action, nil)
		w := httptest.NewRecorder()
		app.handleAction(w, asAdmin(t, r, "bob"))
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
	// Read-only store: an action rewrites the folder, so it takes the write grant.
	ro, _ := SaveStore(app.DB, Store{Name: "ReadOnly", Path: t.TempDir()})
	if _, err := SaveStoreAction(app.DB, StoreAction{Slug: ro.Slug, Name: "decrypt", Command: bin}); err != nil {
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

	if _, err := SaveStoreAction(app.DB, StoreAction{Slug: st.Slug, Name: "x", Command: "diag_decrypt"}); err == nil {
		t.Error("a relative command should be refused — it resolves against whatever directory the server is in")
	}
	if _, err := SaveStoreAction(app.DB, StoreAction{Slug: "nosuch", Name: "x", Command: "/bin/true"}); err == nil {
		t.Error("an action on a store that does not exist should be refused")
	}
	saved, err := SaveStoreAction(app.DB, StoreAction{Slug: st.Slug, Name: "Decrypt Bundle", Command: "/bin/true"})
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
