package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A credential that asks before each call is asked about however a tool
// reaches it: an api tool's credential, and a script's fetch_via: and secret:
// hooks alike. Only the api tool's used to count, so a script spent the key
// with no consent, in chat or unattended.
func TestConsentCoversEveryWayAToolReachesACredential(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "bank", Type: SecureCredBearer, BaseURL: "https://bank.example", RequiresConfirm: true}, "k"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "weather", Type: SecureCredBearer, BaseURL: "https://weather.example"}, "k"); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice"}
	sess.TempTools = []*TempTool{
		{Name: "api_bank", Mode: TempToolModeAPI, Credential: "bank"},
		{Name: "script_via", Mode: TempToolModeShell, HookCapabilities: []string{"fetch", "fetch_via:bank"}},
		{Name: "script_secret", Mode: TempToolModeShell, HookCapabilities: []string{"secret:bank"}},
		{Name: "script_weather", Mode: TempToolModeShell, HookCapabilities: []string{"fetch_via:weather"}},
		{Name: "plain", Mode: TempToolModeShell},
	}
	for name, want := range map[string]bool{"api_bank": true, "script_via": true, "script_secret": true, "script_weather": false, "plain": false} {
		if got := toolAlwaysConfirms(nil, "alice", sess, name); got != want {
			t.Errorf("%s: unattended confirm = %v, want %v", name, got, want)
		}
	}
}

// A key an agent stores is masked in everything that shows or keeps the call,
// and the handler still receives it.
func TestAStoredKeyIsNotShownInTheCall(t *testing.T) {
	args := map[string]any{"name": "gh", "secret": "real-key-value"}
	shown := maskSecretArgs("store_credential_secret", args)
	if shown["secret"] == "real-key-value" || formatToolCall("store_credential_secret", shown) == formatToolCall("store_credential_secret", args) {
		t.Errorf("the key is shown: %v", shown)
	}
	if args["secret"] != "real-key-value" {
		t.Error("the handler's args were changed")
	}
	other := map[string]any{"secret": "x"}
	if got := maskSecretArgs("some_tool", other); got["secret"] != "x" {
		t.Error("only a tool that declares a secret argument is masked")
	}
}

// check_credential shows a credential only to someone allowed it, and its
// call history only as far as the caller's own calls (the owner or an
// administrator sees all of them).
func TestCheckCredentialShowsOnlyWhatTheCallerMaySee(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return store }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prev, prevRoot })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	Secure().Save(SecureCredential{Name: "team", Type: SecureCredBearer, BaseURL: srv.URL, AllowedUsers: []string{"bob", "carol"}}, "k")
	if _, err := Secure().DispatchToolCall(&ToolSession{Username: "carol"}, "team", srv.URL+"/carols-secret-path", "GET", ""); err != nil {
		t.Fatal(err)
	}
	check := func(user string) string {
		out, err := checkCredentialToolDef(&chatTurn{user: user}).Handler(context.Background(), map[string]any{"name": "team", "all_dispatches": true})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := check("alice"); !strings.Contains(out, "not shared") || strings.Contains(out, srv.URL) {
		t.Errorf("alice, not allowed the key, was shown it: %s", out)
	}
	if out := check("bob"); strings.Contains(out, "carols-secret-path") {
		t.Errorf("bob was shown carol's call: %s", out)
	}
	if out := check("carol"); !strings.Contains(out, "carols-secret-path") {
		t.Errorf("carol sees her own call: %s", out)
	}
}

// "Confirm writes" per call: in an unattended run a write through such a
// credential waits for the owner and a read does not; a tool the owner
// approved for unattended runs is already answered.
func TestConfirmWritesInUnattendedRuns(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "tracker", Type: SecureCredBearer, BaseURL: "https://tracker.example", ConfirmWrites: true}, "k"); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice"}
	sess.TempTools = []*TempTool{
		{Name: "tracker_box", Mode: TempToolModeToolbox, Credential: "tracker", Actions: []TempToolAction{{Name: "list"}, {Name: "close", Method: "DELETE"}}},
		{Name: "tracker_box2", Mode: TempToolModeToolbox, Credential: "tracker", Expand: true, Actions: []TempToolAction{{Name: "list"}, {Name: "close", Method: "DELETE"}}},
	}
	g := &autonomousGate{app: &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}, owner: "alice", sess: sess, auto: map[string]bool{}}
	for _, c := range []struct {
		name, args string
		want       bool
	}{
		{"fetch_url_tracker", "method: GET\nurl: https://tracker.example/x", false},
		{"fetch_url_tracker", "url: https://tracker.example/x", false},
		{"fetch_url_tracker", "method: POST\nurl: https://tracker.example/x", true},
		{"tracker_box", "action: list", false},
		{"tracker_box", "action: close", true},
		{"tracker_box2_list", "", false},
		{"tracker_box2_close", "", true},
	} {
		if got := g.writeWaitsForOwner(c.name, c.args); got != c.want {
			t.Errorf("%s %q: waits=%v, want %v", c.name, c.args, got, c.want)
		}
	}
	g.auto["tracker_box"] = true
	if g.writeWaitsForOwner("tracker_box", "action: close") {
		t.Error("a tool the owner approved for unattended runs still waited")
	}
}

// In chat, a read through a credential that asks before writes goes straight
// through; a write asks, and with nobody watching the run it is refused.
func TestConfirmWritesInChat(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "tracker", Type: SecureCredBearer, BaseURL: "https://tracker.example", ConfirmWrites: true}, "k"); err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{user: "alice"}
	gate := turn.confirmFuncFor(&ToolSession{Username: "alice"})
	if !gate("fetch_url_tracker", "method: GET\nurl: https://tracker.example/x") {
		t.Error("a read was stopped")
	}
	if gate("fetch_url_tracker", "method: DELETE\nurl: https://tracker.example/x") {
		t.Error("a write went through with nobody asked")
	}
}

// A run nobody is watching (a channel message, a delegated or pipeline run
// with no viewer) asks through the owner's unattended gate: a call the
// credential asks about is queued, not let through, and the owner's
// ask-before mark on a tool is honoured. These runs used to approve
// everything.
func TestUnwatchedRunsUseTheOwnersGate(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "bank", Type: SecureCredBearer, BaseURL: "https://bank.example", RequiresConfirm: true}, "k"); err != nil {
		t.Fatal(err)
	}
	app := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	sess := &ToolSession{Username: "alice"}
	sess.TempTools = []*TempTool{{Name: "asks", Mode: TempToolModeShell, ConfirmInChat: true}, {Name: "plain", Mode: TempToolModeShell}}
	turn := &chatTurn{app: app, user: "alice"}
	gate, why := turn.runConfirm("agent1", sess)
	if gate("fetch_url_bank", "url: https://bank.example/x") {
		t.Error("a call its credential asks about went through with nobody watching")
	}
	if gate("asks", "") {
		t.Error("a tool marked ask-before-every-call went through with nobody watching")
	}
	// The model is told why, once: "denied by user" on a run nobody watched
	// left a Builder sent work by another agent unable to see what refused it.
	if w := why("asks"); !strings.Contains(w, "asks needs the owner's approval") || !strings.Contains(w, "Authorizations") {
		t.Errorf("the refusal must say it waits for the owner's approval, and where: %q", w)
	}
	if w := why("asks"); w != "" {
		t.Errorf("the reason is given once, then cleared: %q", w)
	}
	if !gate("plain", "") {
		t.Error("an ordinary tool was stopped")
	}
	noApp, noAppWhy := (&chatTurn{user: "alice"}).runConfirm("agent1", sess)
	if noApp("plain", "") {
		t.Error("with no app to queue with, a run refuses rather than approves")
	}
	if !strings.Contains(noAppWhy("plain"), "nowhere to ask") {
		t.Error("a refusal with nowhere to ask must say so")
	}
}

// No run path may hand the agent loop a Confirm that approves everything:
// that is how channel, wake, delegated and pipeline runs ignored every
// confirmation setting.
func TestNoRunApprovesEverything(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "Confirm:") && strings.Contains(string(src), "func(name, args string) bool { return true }") {
			t.Errorf("%s hands the agent loop a Confirm that approves everything", f)
		}
	}
}

// A stranger on a channel meets the owner's share switches: the run executes
// as the owner's account, so without them the owner's saved facts reached
// whoever messaged. Saved facts are off by default; cortex and reference are
// on by default. The owner on their own channel sees everything.
func TestAStrangerOnAChannelMeetsTheShareSwitches(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	agent := AgentRecord{ID: "a1", Owner: "owner"}
	stranger := &chatTurn{user: "owner", ownerUser: "owner", agent: agent, requesterChannel: "channel"}
	if stranger.ownLayerShared(defaultShareNotes) {
		t.Error("a stranger got the owner's saved facts, which are not shared by default")
	}
	if !stranger.ownLayerShared(defaultShareCortex) || !stranger.ownLayerShared(defaultShareReference) {
		t.Error("cortex and reference are shared by default")
	}
	owner := &chatTurn{user: "owner", ownerUser: "owner", agent: agent, requesterChannel: "channel", requesterOwnerHandle: true}
	if !owner.ownLayerShared(defaultShareNotes) {
		t.Error("the owner on their own channel sees their own facts")
	}
	web := &chatTurn{user: "owner", ownerUser: "owner", agent: agent}
	if !web.ownLayerShared(defaultShareNotes) {
		t.Error("the owner's own web run sees their own facts")
	}
}

// Every run that can be refused an approval tells its model why: the loop's
// own fallback is "denied by user", which on a run nobody watched is not true,
// and a Builder sent work by another agent gave up on a tool over refusals it
// could not see.
func TestEveryRefusingRunSaysWhy(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"runner.go", "ConfirmRefusal:      t.confirmRefusal,"},
		{"runner_worker.go", "ConfirmRefusal:      t.confirmRefusal,"},
		{"agent_dispatch.go", "ConfirmRefusal: gate.refusal,"},
		{"agent_dispatch.go", "ConfirmRefusal:      sd.refusal,"},
		{"agent_dispatch.go", "sd.refusal = confirmRefusalFrom(sd.ctx)"},
		{"scheduled_updates.go", "ConfirmRefusal:      fr.gate.refusal,"},
		{"agents_grouped_tool.go", "ConfirmRefusal:      ar.runRefusalFn,"},
		{"pipeline_tools.go", "ConfirmRefusal:      stageRefusal,"},
		{"standing_runner.go", "withConfirmRefusal(ctx, gate.refusal)"},
	} {
		// Spacing-blind: gofmt realigns a literal's keys whenever a field
		// moves in or out of it, and that is not the code changing.
		if !strings.Contains(collapseSpaces(readFile(t, c.file)), collapseSpaces(c.want)) {
			t.Errorf("%s no longer has %q", c.file, c.want)
		}
	}
}

// collapseSpaces folds every run of spaces and tabs to one space.
func collapseSpaces(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' }), " ")
}
