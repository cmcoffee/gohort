package filestore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// pullFixture is Intake (cases arrive and are prepared) and Analysis (pulls
// prepared cases from Intake). CASE-1 is prepared; CASE-2 is not.
func pullFixture(t *testing.T) (*FileStoreApp, Store, Store) {
	t.Helper()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	intakeRoot, analysisRoot := t.TempDir(), t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(intakeRoot, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CASE-1/raw.bin", "ENCRYPTED")
	write("CASE-1/prepared/app.log", "ERROR disk full")
	write("CASE-1/prepared/node2/sys.log", "kernel: oops")
	os.Symlink("/etc/passwd", filepath.Join(intakeRoot, "CASE-1", "prepared", "escape"))
	write("CASE-2/raw.bin", "ENCRYPTED")
	intake, err := SaveStore(app.DB, Store{Name: "Intake", Path: intakeRoot, Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := SaveStore(app.DB, Store{Name: "Analysis", Path: analysisRoot, PullFrom: intake.Slug})
	if err != nil {
		t.Fatal(err)
	}
	return app, intake, analysis
}

// A pull copies the prepared part of a case, not the raw download beside it
// and not what a link points at, leaves no staging folder, and refuses to
// overwrite a copy unless asked.
func TestAPullCopiesThePreparedPartWhole(t *testing.T) {
	app, _, analysis := pullFixture(t)
	job, err := app.pullJobFor(analysis, "CASE-1", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := job.run(context.Background())
	if err != nil || !strings.Contains(out, "Copied CASE-1 (prepared/) from Intake into Analysis: 2 files") || !strings.Contains(out, "1 link(s)") {
		t.Fatalf("pull: %q %v", out, err)
	}
	got := func(rel string) string { b, _ := os.ReadFile(filepath.Join(analysis.Path, rel)); return string(b) }
	if got("CASE-1/app.log") != "ERROR disk full" || got("CASE-1/node2/sys.log") != "kernel: oops" {
		t.Error("the prepared files did not arrive")
	}
	for _, rel := range []string{"CASE-1/raw.bin", "CASE-1/escape", stagingPrefix + "CASE-1"} {
		if _, err := os.Lstat(filepath.Join(analysis.Path, rel)); err == nil {
			t.Errorf("%s should not be in Analysis", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(analysis.Path), "x")); err == nil {
		t.Error("wrote outside the store")
	}

	// A second bundle of the same case is prepared in Intake, and the first
	// has since expired there. Pulling again adds the new one, updates what
	// changed, and keeps what is already here.
	src, _ := analysis.pullSource(app.DB)
	os.RemoveAll(filepath.Join(src.Path, "CASE-1", "prepared", "node2"))
	os.WriteFile(filepath.Join(src.Path, "CASE-1", "prepared", "bundle2.log"), []byte("second bundle"), 0o644)
	os.WriteFile(filepath.Join(src.Path, "CASE-1", "prepared", "app.log"), []byte("ERROR disk full (rotated)"), 0o644)
	again, _ := app.pullJobFor(analysis, "CASE-1", "alice", false)
	out, err = again.run(context.Background())
	if err != nil || !strings.Contains(out, "1 new, 1 updated, nothing removed") {
		t.Fatalf("a second pull of the case: %q %v", out, err)
	}
	if got("CASE-1/bundle2.log") != "second bundle" || got("CASE-1/app.log") != "ERROR disk full (rotated)" || got("CASE-1/node2/sys.log") != "kernel: oops" {
		t.Error("adding to the case lost or failed to update a file")
	}

	// replace clears the case first.
	replace, _ := app.pullJobFor(analysis, "CASE-1", "alice", true)
	if _, err := replace.run(context.Background()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(analysis.Path, "CASE-1", "node2")); err == nil {
		t.Error("replace kept a folder the new copy does not have")
	}
	entries, _ := os.ReadDir(analysis.Path)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

// What a pull refuses, before it writes anything: a case not prepared yet, a
// name that is a path, a destination with no room, and a stopped copy.
func TestAPullRefusesBeforeItWrites(t *testing.T) {
	app, _, analysis := pullFixture(t)
	for name, want := range map[string]string{
		"CASE-2":    "has not been prepared",
		"../CASE-1": "one name with no path",
		"CASE-9":    "no case called",
		".hidden":   "one name with no path",
	} {
		job, _ := app.pullJobFor(analysis, name, "alice", false)
		if _, err := job.run(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	full := analysis
	full.MaxGB = 1
	pretendUsage(t, full, 2<<30)
	job, _ := app.pullJobFor(full, "CASE-1", "alice", false)
	if _, err := job.run(context.Background()); err == nil || !strings.Contains(err.Error(), "Nothing was deleted") {
		t.Errorf("a pull into a full store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job, _ = app.pullJobFor(analysis, "CASE-1", "alice", false)
	if _, err := job.run(ctx); err == nil {
		t.Error("a stopped pull reported success")
	}
	// One that fails partway, after the staging folder exists: a file it
	// cannot read. Nothing of it may stay.
	if os.Geteuid() != 0 {
		src, _ := analysis.pullSource(app.DB)
		dir := filepath.Join(src.Path, "CASE-3", "prepared")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "a.log"), []byte("fine"), 0o644)
		os.WriteFile(filepath.Join(dir, "z.log"), []byte("locked"), 0o000)
		job, _ = app.pullJobFor(analysis, "CASE-3", "alice", false)
		if _, err := job.run(context.Background()); err == nil || !strings.Contains(err.Error(), "nothing was kept") {
			t.Errorf("a pull that failed partway: %v", err)
		}
	}
	entries, _ := os.ReadDir(analysis.Path)
	if len(entries) != 0 {
		t.Errorf("a refused, stopped or failed pull left %d entries in Analysis", len(entries))
	}
}

// A pull link has to name another store that exists, and a plain folder.
func TestAPullLinkIsCheckedWhenSaved(t *testing.T) {
	app, intake, _ := pullFixture(t)
	for _, bad := range []Store{
		{Name: "Self", Slug: "self", Path: t.TempDir(), PullFrom: "self"},
		{Name: "Nowhere", Path: t.TempDir(), PullFrom: "missing"},
		{Name: "Escape", Path: t.TempDir(), PullFrom: intake.Slug, PullSubpath: "../etc"},
	} {
		if _, err := SaveStore(app.DB, bad); err == nil {
			t.Errorf("%s saved: %+v", bad.Name, bad)
		}
	}
	whole, err := SaveStore(app.DB, Store{Name: "Whole", Path: t.TempDir(), PullFrom: intake.Slug, PullSubpath: "."})
	if err != nil || whole.pullSubpath() != "" {
		t.Errorf("a whole-case link: %v %q", err, whole.pullSubpath())
	}
}

// The agent's tool arrives with the destination store, names the source's
// cases (not their contents), and copies through the same path.
func TestAnAgentPullsThroughTheAttachedStore(t *testing.T) {
	app, intake, analysis := pullFixture(t)
	src := storeSource{app: app}
	sess := &ToolSession{Ctx: context.Background(), Username: "alice"}
	var pull *AgentToolDef
	for _, td := range src.ItemToolsWithSession(sess, "alice", analysis.Slug) {
		if td.Tool.Name == "pull_"+analysis.Slug {
			td := td
			pull = &td
		}
	}
	if pull == nil {
		t.Fatal("no pull tool on a store with a pull link")
	}
	if !strings.Contains(pull.Tool.Description, "CASE-1") || strings.Contains(pull.Tool.Description, "disk full") {
		t.Errorf("the description should name cases, never contents: %s", pull.Tool.Description)
	}
	for _, td := range src.ItemToolsWithSession(sess, "alice", intake.Slug) {
		if strings.HasPrefix(td.Tool.Name, "pull_") {
			t.Error("a store with no pull link offered a pull tool")
		}
	}
	out, err := pull.Handler(context.Background(), map[string]any{"case": "CASE-1"})
	if err != nil || !strings.Contains(out, "Copied CASE-1") {
		t.Fatalf("agent pull: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(analysis.Path, "CASE-1", "app.log")); err != nil {
		t.Error("the agent's pull did not land")
	}
}

// The panel starts a pull, follows it to its outcome, and lists the cases
// with whether each is ready and already here.
func TestThePanelPullsAndReportsTheOutcome(t *testing.T) {
	app, _, analysis := pullFixture(t)
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "admin-a", "pw-admin-123", true)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "admin-a")
	withCookie := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		app.handlePull(w, r)
		return w
	}
	list := withCookie(http.MethodGet, "/filestore/api/pull/cases?slug="+analysis.Slug)
	if !strings.Contains(list.Body.String(), "prepared/ ready to copy") || !strings.Contains(list.Body.String(), "not prepared yet") {
		t.Fatalf("cases: %d %s", list.Code, list.Body.String())
	}
	start := withCookie(http.MethodPost, "/filestore/api/pull?slug="+analysis.Slug+"&case=CASE-1")
	if !strings.Contains(start.Body.String(), `"running":true`) {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		p := withCookie(http.MethodGet, "/filestore/api/pull/progress?slug="+analysis.Slug+"&case=CASE-1").Body.String()
		if strings.Contains(p, "outcome") {
			if !strings.Contains(p, "Copied CASE-1") {
				t.Errorf("outcome: %s", p)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the pull never reported an outcome")
		}
		time.Sleep(20 * time.Millisecond)
	}
	list = withCookie(http.MethodGet, "/filestore/api/pull/cases?slug="+analysis.Slug)
	if !strings.Contains(list.Body.String(), "in Analysis, last added to") {
		t.Errorf("the case does not read as pulled: %s", list.Body.String())
	}
}
