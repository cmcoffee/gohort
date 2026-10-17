package servitor

// Out of Servitor: a workspace becomes an agent with its members as Sources,
// and an evidence bundle is copied into a file store folder. Neither touches
// the original.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/snugforge/kvlite"
)

func movingOutApp(t *testing.T) (*Servitor, Database) {
	t.Helper()
	prevAuth, prevRoot := AuthDB, RootDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	AuthDB = func() Database { return auth }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	return app, UserDB(app.DB, "alice")
}

func TestAWorkspaceBecomesAnAgentWithItsMembersAsSources(t *testing.T) {
	app, udb := movingOutApp(t)
	sources := fakeSources(t)
	var made appagents.UserAgent
	prev := appagents.CreateUserAgent
	appagents.CreateUserAgent = func(owner string, a appagents.UserAgent) (string, string, error) {
		if owner != "alice" {
			t.Errorf("the agent was made for %q", owner)
		}
		made = a
		return "ag1", a.Name, nil
	}
	t.Cleanup(func() { appagents.CreateUserAgent = prev })

	udb.Set(applianceTable, "s1", Appliance{ID: "s1", Name: "sched-01", Type: "ssh", Owner: "alice"})
	udb.Set(applianceTable, "r1", Appliance{ID: "r1", Name: "scheduler-code", Type: "repo", Owner: "alice"})
	udb.Set(applianceTable, "w1", Appliance{ID: "w1", Name: "cluster", Type: "workspace", Owner: "alice",
		Members:      []string{"s1", "r1", "gone"},
		MemberRoles:  map[string]string{"s1": "scheduler + primary DB"},
		MemberLinks:  []MemberLink{{From: "r1", Rel: MemberRelCodeFor, To: "s1"}},
		Instructions: "Times are UTC."})

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		app.handleWorkspaceAgent(w, reqAs(t, http.MethodPost, "/api/workspace/agent", "alice", body))
		return w.Code, w.Body.String()
	}
	code, body := post(`{"appliance_id":"w1"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		URL      string   `json:"url"`
		Attached []string `json:"attached"`
		Skipped  []string `json:"skipped"`
	}
	json.Unmarshal([]byte(body), &got)
	if got.URL != "/orchestrate/agent/ag1" || len(got.Attached) != 2 || len(got.Skipped) != 1 {
		t.Errorf("reply: %s", body)
	}
	if n := len(sources["alice/ag1"]); n != 2 {
		t.Errorf("the agent has %d Sources, want the two reachable members", n)
	}
	for _, want := range []string{"cluster", "sched-01 (SSH system): scheduler + primary DB", "scheduler-code is the code for sched-01", "Times are UTC."} {
		if !strings.Contains(made.Prompt, want) {
			t.Errorf("the instructions lack %q:\n%s", want, made.Prompt)
		}
	}
	if strings.Contains(made.Prompt, "—") {
		t.Error("an em-dash in the agent's instructions")
	}
	if made.MemoryMode != "agent" {
		t.Errorf("memory mode %q", made.MemoryMode)
	}
	var ws Appliance
	if !udb.Get(applianceTable, "w1", &ws) || ws.Type != "workspace" {
		t.Error("the workspace was changed")
	}
	if code, _ := post(`{"appliance_id":"s1"}`); code != http.StatusBadRequest {
		t.Errorf("an agent was made from an ssh system: %d", code)
	}
	w := httptest.NewRecorder()
	app.handleWorkspaceAgent(w, reqAs(t, http.MethodPost, "/api/workspace/agent", "bob", `{"appliance_id":"w1"}`))
	if w.Code != http.StatusNotFound {
		t.Errorf("someone else's workspace was turned into their agent: %d", w.Code)
	}
}

// fakeDrop is a file-store side holding one evidence store in a folder.
type fakeDrop struct {
	appstores.Host
	root string
}

func (fakeDrop) EvidenceStores(user string, isAdmin bool) []appstores.EvidenceStore {
	return []appstores.EvidenceStore{{Store: "cases", Name: "Cases"}}
}

func (d fakeDrop) AddEvidenceFolder(ctx context.Context, user string, isAdmin bool, store, name string, need int64, fill func(dir string) error) (string, error) {
	staging := filepath.Join(d.root, ".incoming-x")
	os.MkdirAll(staging, 0o755)
	if err := fill(staging); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	return name, os.Rename(staging, filepath.Join(d.root, name))
}

func TestABundleIsCopiedIntoAFileStore(t *testing.T) {
	app, udb := movingOutApp(t)
	prevDB, prevProv := BundleFilesDB, appstores.Provider
	BundleFilesDB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	appstores.Provider = fakeDrop{root: root}
	t.Cleanup(func() { BundleFilesDB, appstores.Provider = prevDB, prevProv })

	stage := t.TempDir()
	os.MkdirAll(filepath.Join(stage, "var/log"), 0o755)
	os.WriteFile(filepath.Join(stage, "var/log/messages"), []byte("Mar 14 02:00:00 web1 cron: stalled\n"), 0o644)
	os.WriteFile(filepath.Join(stage, "core.dump"), []byte("\x00\x01\x02binary\x00"), 0o644)
	if _, err := bundle.Open("alice", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	udb.Set(applianceTable, "b1", Appliance{ID: "b1", Name: "incident", Type: "bundle", Owner: "alice", BundleState: bundleStateReady})

	call := func(method, target, user string) (int, string) {
		w := httptest.NewRecorder()
		h := app.handleBundleMove
		if strings.Contains(target, "move-targets") {
			h = app.handleBundleMoveTargets
		}
		h(w, reqAs(t, method, target, user, ""))
		return w.Code, w.Body.String()
	}
	if code, body := call(http.MethodGet, "/api/bundle/move-targets?appliance_id=b1", "alice"); code != http.StatusOK || !strings.Contains(body, `"store":"cases"`) {
		t.Fatalf("targets: %d %s", code, body)
	}
	if code, _ := call(http.MethodPost, "/api/bundle/move?appliance_id=b1&store=cases", "bob"); code != http.StatusNotFound {
		t.Errorf("someone else moved the bundle: %d", code)
	}
	if code, body := call(http.MethodPost, "/api/bundle/move?appliance_id=b1&store=elsewhere", "alice"); code != http.StatusBadRequest {
		t.Errorf("moved to a store that is not offered: %d %s", code, body)
	}
	if code, body := call(http.MethodPost, "/api/bundle/move?appliance_id=b1&store=cases", "alice"); code != http.StatusOK || !strings.Contains(body, `"running":true`) {
		t.Fatalf("start: %d %s", code, body)
	}
	var outcome string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, body := call(http.MethodGet, "/api/bundle/move?appliance_id=b1&store=cases", "alice")
		var st map[string]string
		json.Unmarshal([]byte(body), &st)
		if outcome = st["outcome"]; outcome != "" {
			break
		}
	}
	if !strings.Contains(outcome, "Moved to Cases, folder incident") || !strings.Contains(outcome, "core.dump") {
		t.Fatalf("outcome: %q", outcome)
	}
	data, err := os.ReadFile(filepath.Join(root, "incident", "var", "log", "messages"))
	if err != nil || string(data) != "Mar 14 02:00:00 web1 cron: stalled\n" {
		t.Errorf("the copied file: %v %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(root, "incident", "core.dump")); !os.IsNotExist(err) {
		t.Error("a binary with no stored content was written as a file")
	}
	if bundle.Open("alice", "b1").FileCount() != 2 {
		t.Error("the bundle was changed by moving it")
	}
	// Another row's store answers idle, so only the row that ran shows it.
	if _, body := call(http.MethodGet, "/api/bundle/move?appliance_id=b1&store=other", "alice"); strings.TrimSpace(body) != "{}" {
		t.Errorf("another store's row reported this move: %s", body)
	}
}
