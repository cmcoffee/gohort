package temptool

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/buildledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tool test lands in the build ledger with the KIND of failure, so runs
// that broke the same way cluster. A repeat the test cache serves is not a
// run and is not recorded twice.
func TestToolTestLandsInBuildLedger(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	sess := newTestSession()
	sess.AgentID = "builder"
	injectBrokenMoltbook(t, sess)

	args := map[string]any{"name": "moltbook"}
	if _, err := testGrouped(args, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := testGrouped(args, sess); err != nil {
		t.Fatal(err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Verifications != 1 || rep.Fails != 1 {
		t.Fatalf("ledger has %d verifications, %d failed; want the one failed run", rep.Verifications, rep.Fails)
	}
	ep := rep.Recent[0]
	if ep.Owner != "alice" || ep.Kind != buildledger.KindTool || ep.Target != "moltbook" || ep.Green {
		t.Fatalf("episode = %+v", ep)
	}
	if !reflect.DeepEqual(ep.Classes, []string{"unsent-param"}) {
		t.Fatalf("classes = %v, want [unsent-param]", ep.Classes)
	}
}

// A shell tool tested without cases found nothing wrong and proved nothing:
// unproven, not failed.
func TestShellTestWithoutCasesIsUnproven(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	sess := newTestSession()
	if err := sess.AppendTempTool(&TempTool{Name: "echoer", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := testGrouped(map[string]any{"name": "echoer"}, sess); err != nil {
		t.Fatal(err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Unproven != 1 || rep.Fails != 0 || len(rep.Classes) != 1 || rep.Classes[0].Class != "no-cases" {
		t.Fatalf("report = %+v", rep)
	}
}

// A run with no error can still have failed: dispatch returns a shell tool's
// non-zero exit and an api tool's error status as output. An api tool whose
// pipe replaced the status line cannot say either way.
func TestCheckRunReadsTheOutputNotJustTheError(t *testing.T) {
	shell := TempTool{Name: "s", CommandTemplate: "python3 x.py"}
	api := TempTool{Name: "a", Mode: TempToolModeAPI, CommandTemplate: "https://x.test/"}
	piped := api
	piped.ResponsePipe = ".items"
	for _, c := range []struct {
		name string
		tt   TempTool
		out  string
		want RunCheck
	}{
		{"shell clean", shell, "made 3 rows", RunCheck{Known: true, OK: true}},
		{"shell exit", shell, "Traceback...\n[exit: exit status 1]", RunCheck{Known: true, Class: "run-exit"}},
		{"shell silent", shell, "  ", RunCheck{Known: true, Class: "run-hollow"}},
		{"api 2xx", api, "HTTP 200 OK\n{}", RunCheck{Known: true, OK: true}},
		{"api 404", api, "HTTP 404 Not Found\n{}", RunCheck{Known: true, Class: "live-status"}},
		{"api piped", piped, "[]", RunCheck{}},
	} {
		got := CheckRun(c.tt, c.out)
		if got.Known != c.want.Known || got.OK != c.want.OK || got.Class != c.want.Class {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.Known && !got.OK && got.Why == "" {
			t.Errorf("%s: a failure with no reason", c.name)
		}
	}
}

// The builder fired the last waiting write by hand, the log said "tool
// verified", and the ledger the graders read stayed red: only test wrote to
// it. A direct 2xx of the last unfired write now closes the episode green.
func TestDirectWriteGreensTheLedger(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	f, sess := newFakeAPI(t, "tr")
	if _, err := createGrouped(map[string]any{
		"name": "translate", "description": "d", "mode": "toolbox", "credential": "tr",
		"actions": []any{
			map[string]any{"name": "post", "url_template": f.srv.URL + "/translate", "method": "POST",
				"body_template": `{"text": {text}}`,
				"params":        map[string]any{"text": map[string]any{"type": "string"}}},
		},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := testGrouped(map[string]any{"name": "translate"}, sess); err != nil {
		t.Fatalf("test: %v", err)
	}
	if rep := buildledger.Read(time.Time{}); rep.Green != 0 {
		t.Fatalf("an unfired write must not be green yet: %+v", rep)
	}
	tt := sess.LookupTempTool("translate")
	if _, err := dispatchTempTool(sess, tt, map[string]any{"action": "post", "text": "hola"}); err != nil {
		t.Fatalf("direct call: %v", err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Green != 1 || len(rep.Recent) != 1 || !rep.Recent[0].Green || rep.Recent[0].Target != "translate" {
		t.Fatalf("the direct 2xx must close a green episode: %+v", rep)
	}
}

// TestToolboxHandlerLiveResolvesAfterRename pins the mid-turn staleness fix:
// an agent-owned toolbox is pinned into the static catalog and skipped by the
// per-round dynamic-tool refresh, so the AgentToolDef built at turn start would
// keep dispatching the frozen snapshot. After a tool_def update renames an
// action on the SESSION record, the already-built handler must reflect the live
// record — dispatch + help resolve against current actions, not the snapshot.
func TestToolboxHandlerLiveResolvesAfterRename(t *testing.T) {
	sess := newTestSession()
	orig := &TempTool{
		Name:       "moltbook",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "post_message", Description: "old post action", URLTemplate: "https://x.test/p", Method: "GET"},
		},
	}
	if err := sess.AppendTempTool(orig); err != nil {
		t.Fatal(err)
	}

	// Handler built at "turn start" from the snapshot.
	def := agentToolFromTemp(sess, orig)

	// Simulate a mid-turn tool_def rename: the session record now carries
	// create_post instead of post_message (RemoveTempTool + AppendTempTool is
	// exactly what createToolboxGrouped does).
	sess.RemoveTempTool("moltbook")
	renamed := &TempTool{
		Name:       "moltbook",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "create_post", Description: "new post action", URLTemplate: "https://x.test/p", Method: "GET"},
		},
	}
	if err := sess.AppendTempTool(renamed); err != nil {
		t.Fatal(err)
	}

	// help must list the renamed action and drop the stale one.
	help, err := def.Handler(context.Background(), map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(help, "create_post") {
		t.Errorf("help does not reflect the live record (missing create_post):\n%s", help)
	}
	if strings.Contains(help, "post_message") {
		t.Errorf("help still lists the stale action post_message:\n%s", help)
	}

	// A call to the now-removed action must be rejected as unknown — proof the
	// frozen snapshot is no longer the dispatch source.
	if _, err := def.Handler(context.Background(), map[string]any{"action": "post_message"}); err == nil ||
		!strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected unknown-action error for the removed action, got: %v", err)
	}
}

// TestTempToolCaps pins the capability tier per mode — the value the loop
// cap-gates on and the live-resolve guard compares against.
func TestTempToolCaps(t *testing.T) {
	cases := []struct {
		name string
		tt   *TempTool
		want []Capability
	}{
		{"api plain", &TempTool{Mode: TempToolModeAPI}, []Capability{CapNetwork}},
		{"api with pipe", &TempTool{Mode: TempToolModeAPI, ResponsePipe: "jq ."}, []Capability{CapNetwork, CapExecute}},
		{"shell", &TempTool{Mode: TempToolModeShell}, []Capability{CapExecute}},
		{"default/empty", &TempTool{}, []Capability{CapExecute}},
	}
	for _, c := range cases {
		got := tempToolCaps(c.tt)
		if len(got) != len(c.want) || !capsSubset(c.want, got) || !capsSubset(got, c.want) {
			t.Errorf("%s: tempToolCaps = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLiveResolveCapGuard proves the guard: an api tool that GROWS its cap
// profile mid-turn (gains a response_pipe → CapExecute) is NOT eligible for
// live dispatch under the snapshot's [CapNetwork] gate, so the snapshot runs
// until next turn; a non-cap edit stays eligible.
func TestLiveResolveCapGuard(t *testing.T) {
	snapCaps := tempToolCaps(&TempTool{Mode: TempToolModeAPI}) // [CapNetwork]

	grown := tempToolCaps(&TempTool{Mode: TempToolModeAPI, ResponsePipe: "jq ."})
	if capsSubset(grown, snapCaps) {
		t.Error("cap-growing edit must NOT be live-dispatch eligible under the snapshot gate")
	}

	sameShape := tempToolCaps(&TempTool{Mode: TempToolModeAPI, CommandTemplate: "https://x/new"})
	if !capsSubset(sameShape, snapCaps) {
		t.Error("non-cap edit must stay live-dispatch eligible")
	}
}

// TestToolboxDisabledActionQuarantined verifies a disabled action is dropped
// from the group (not offered, not dispatchable) while the rest keep working.
func TestToolboxDisabledActionQuarantined(t *testing.T) {
	sess := newTestSession()
	tt := &TempTool{
		Name:       "svc",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "read_thing", Description: "ok", URLTemplate: "https://x.test/r", Method: "GET"},
			{Name: "broken", Description: "quarantined", URLTemplate: "https://x.test/b", Method: "GET", Disabled: true},
		},
	}
	if err := sess.AppendTempTool(tt); err != nil {
		t.Fatal(err)
	}
	def := agentToolFromTemp(sess, tt)

	help, err := def.Handler(context.Background(), map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if strings.Contains(help, "broken") {
		t.Errorf("disabled action should not appear in help:\n%s", help)
	}
	if !strings.Contains(help, "read_thing") {
		t.Errorf("live action missing from help:\n%s", help)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"action": "broken"}); err == nil ||
		!strings.Contains(err.Error(), "unknown action") {
		t.Errorf("disabled action should be unroutable, got: %v", err)
	}
}

// An edit made by ANOTHER session reaches a session that loaded the tool
// before it. Observed: a delegated Builder rewrote a failing script, and the
// agent that asked for the fix ran the old one again, same traceback, same file.
func TestAToolEditedElsewhereRunsAsEdited(t *testing.T) {
	caller := newTestSession()
	caller.AgentID = "wren"
	old := TempTool{Name: "make_song", Mode: "shell", ScriptBody: "print('old')", CommandTemplate: "python3 {script}"}
	if err := AdminPersistTempTool(caller.DB, "alice", old); err != nil {
		t.Fatal(err)
	}
	loaded := old
	if err := caller.AppendTempTool(&loaded); err != nil {
		t.Fatal(err)
	}
	if got := currentTempTool(caller, "make_song"); got.ScriptBody != "print('old')" {
		t.Fatalf("nothing changed, the session copy runs: %q", got.ScriptBody)
	}

	// Builder's session saves a new body; the caller's session copy is untouched.
	fixed := old
	fixed.ScriptBody = "print('new')"
	if !UpdatePersistentTempTool(caller.DB, "alice", fixed) {
		t.Fatal("the edit was not stored")
	}
	if got := currentTempTool(caller, "make_song"); got.ScriptBody != "print('new')" {
		t.Errorf("the stored edit should run, got %q", got.ScriptBody)
	}

	// A row this agent would not load is never taken: scoped elsewhere, or off.
	if !SetUserToolScopeAgents(caller.DB, "alice", "make_song", []string{"someone_else"}) {
		t.Fatal("scope not set")
	}
	if got := currentTempTool(caller, "make_song"); got.ScriptBody != "print('old')" {
		t.Errorf("a row scoped to another agent must not replace this agent's copy, got %q", got.ScriptBody)
	}
	off := fixed
	off.Disabled = true
	SetUserToolScopeAgents(caller.DB, "alice", "make_song", nil)
	UpdatePersistentTempTool(caller.DB, "alice", off)
	if got := currentTempTool(caller, "make_song"); got.ScriptBody != "print('old')" {
		t.Errorf("a tool turned off is not picked up mid-turn, got %q", got.ScriptBody)
	}
}

func TestCatalogNamesOf(t *testing.T) {
	tb := &TempTool{
		Name: "vapi_calls",
		Mode: TempToolModeToolbox,
		Actions: []TempToolAction{
			{Name: "place_call"}, {Name: "end_call"},
		},
	}
	got := strings.Join(catalogNamesOf(tb), ",")
	want := "vapi_calls,vapi_calls_place_call,vapi_calls_end_call"
	if got != want {
		t.Errorf("toolbox names: got %q want %q", got, want)
	}
	// A plain api tool contributes only its own name.
	api := &TempTool{Name: "get_weather", Mode: TempToolModeAPI}
	if got := strings.Join(catalogNamesOf(api), ","); got != "get_weather" {
		t.Errorf("api names: got %q", got)
	}
}

func TestCheckCatalogNameCollision(t *testing.T) {
	sess := &ToolSession{}
	if err := sess.AppendTempTool(&TempTool{
		Name: "vapi_calls", Mode: TempToolModeToolbox,
		Actions: []TempToolAction{{Name: "place_call"}},
	}); err != nil {
		t.Fatalf("seed toolbox: %v", err)
	}

	// The live bug: a standalone tool named after an existing toolbox action.
	err := CheckCatalogNameCollision(sess, "vapi_calls_place_call", nil)
	if err == nil {
		t.Fatal("expected a collision against the toolbox's expanded action name")
	}
	if !strings.Contains(err.Error(), "vapi_calls") {
		t.Errorf("error should name the owning toolbox: %v", err)
	}

	// A name nobody publishes is fine.
	if err := CheckCatalogNameCollision(sess, "vapi_calls_get_recording", nil); err != nil {
		t.Errorf("unexpected collision: %v", err)
	}

	// Re-authoring the SAME record overwrites — the iteration path, not a collision.
	if err := CheckCatalogNameCollision(sess, "vapi_calls", []TempToolAction{{Name: "place_call"}}); err != nil {
		t.Errorf("same-name re-author must be allowed: %v", err)
	}

	// The other direction: a NEW toolbox whose action would mint an existing
	// standalone tool's name.
	if err := sess.AppendTempTool(&TempTool{Name: "vapi_admin_list_keys", Mode: TempToolModeAPI}); err != nil {
		t.Fatalf("seed api tool: %v", err)
	}
	err = CheckCatalogNameCollision(sess, "vapi_admin", []TempToolAction{{Name: "list_keys"}})
	if err == nil {
		t.Fatal("expected a collision: toolbox action would mint an existing tool name")
	}
}

func TestTempToolNeedsConfirm(t *testing.T) {
	cases := []struct {
		name string
		tt   *TempTool
		want bool
	}{
		{"benign shell fetch", &TempTool{Mode: "", HookCapabilities: []string{"fetch"}}, false},
		{"benign read-only hooks", &TempTool{HookCapabilities: []string{"fetch", "log", "browse_page"}}, false},
		{"plain shell no caps", &TempTool{}, false},
		{"api mode no credential", &TempTool{Mode: TempToolModeAPI}, true},
		{"unknown credential fails closed", &TempTool{Credential: "graph"}, true},
		{"raw network", &TempTool{RawNetwork: true}, true},
		{"secret capability", &TempTool{HookCapabilities: []string{"fetch", "secret:token"}}, true},
		{"fetch_via credential", &TempTool{HookCapabilities: []string{"fetch_via:cred"}}, true},
		{"nil", nil, true},
	}
	for _, c := range cases {
		if got := tempToolNeedsConfirm(c.tt); got != c.want {
			t.Errorf("%s: tempToolNeedsConfirm=%v want %v", c.name, got, c.want)
		}
	}
}

// TestTempToolNeedsConfirmCredentialTier pins the credential-tier deferral: a
// credentialed temp tool (api / toolbox) inherits the credential's own
// "Require confirm before each call" toggle — the same contract the
// auto-generated call_<cred> bridge tools honor. Blanket-true here made the
// same credential run unattended through its bridge tool while its toolbox was
// refused on every scheduled/standing fire.
func TestTempToolNeedsConfirmCredentialTier(t *testing.T) {
	prev := AuthDB
	AuthDB = func() Database { return &DBase{Store: kvlite.MemStore()} }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "quiet_api", Type: SecureCredBearer,
		BaseURL: "https://api.example.com"}, "tok"); err != nil {
		t.Fatalf("save quiet cred: %v", err)
	}
	if err := Secure().Save(SecureCredential{Name: "loud_api", Type: SecureCredBearer,
		BaseURL: "https://api.example.com", RequiresConfirm: true}, "tok"); err != nil {
		t.Fatalf("save loud cred: %v", err)
	}
	if tempToolNeedsConfirm(&TempTool{Mode: TempToolModeAPI, Credential: "quiet_api"}) {
		t.Fatal("credential without Require-confirm must run unattended (NeedsConfirm=false)")
	}
	if !tempToolNeedsConfirm(&TempTool{Mode: TempToolModeToolbox, Credential: "loud_api"}) {
		t.Fatal("Require-confirm credential must keep the gate (NeedsConfirm=true)")
	}
	// RawNetwork stays consequential regardless of the credential's tier.
	if !tempToolNeedsConfirm(&TempTool{Credential: "quiet_api", RawNetwork: true}) {
		t.Fatal("RawNetwork must gate even with a quiet credential")
	}
}

// A tool on the user's OWN credential takes that credential's tier. Only global
// credentials were looked at, so it was never found, failed closed, and asked
// before every call; test would not live-probe it either.
func TestAToolOnAUsersOwnCredentialTakesItsTier(t *testing.T) {
	prev := AuthDB
	AuthDB = func() Database { return &DBase{Store: kvlite.MemStore()} }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "own_gen", Owner: "alice", Type: SecureCredBearer,
		BaseURL: "https://gen.example.com"}, "tok"); err != nil {
		t.Fatalf("save own cred: %v", err)
	}
	tt := &TempTool{Mode: TempToolModeAPI, Credential: "own_gen"}
	if tempToolNeedsConfirm(tt, "alice") {
		t.Error("the owner's quiet credential should let the tool run unattended")
	}
	if !tempToolNeedsConfirm(tt, "bob") || !tempToolNeedsConfirm(tt) {
		t.Error("for anyone else, or with no user, the credential is not theirs to resolve: fail closed")
	}
	if !NeedsConfirm(tt) || NeedsConfirm(tt, "alice") {
		t.Error("the exported form takes the user the same way")
	}
}

// fetch_via through a credential takes that credential's tier, as an api tool
// on it does. Blanket-true made a shell tool on a quiet credential ask before
// every call, so test would never run it.
func TestFetchViaTakesTheCredentialsTier(t *testing.T) {
	prev := AuthDB
	AuthDB = func() Database { return &DBase{Store: kvlite.MemStore()} }
	defer func() { AuthDB = prev }()
	Secure().Save(SecureCredential{Name: "quiet_gen", Owner: "alice", Type: SecureCredBearer, BaseURL: "https://gen.example.com"}, "tok")
	Secure().Save(SecureCredential{Name: "loud_gen", Owner: "alice", Type: SecureCredBearer, BaseURL: "https://gen.example.com", RequiresConfirm: true}, "tok")
	shell := func(caps ...string) *TempTool {
		return &TempTool{ScriptBody: "print(1)", HookCapabilities: append([]string{"fetch", "log"}, caps...)}
	}
	if tempToolNeedsConfirm(shell("fetch_via:quiet_gen"), "alice") {
		t.Error("fetch_via on a quiet credential should run unattended")
	}
	if !tempToolNeedsConfirm(shell("fetch_via:loud_gen"), "alice") {
		t.Error("fetch_via on a Require-confirm credential keeps the gate")
	}
	if !tempToolNeedsConfirm(shell("fetch_via:quiet_gen"), "bob") || !tempToolNeedsConfirm(shell("fetch_via:nobody"), "alice") {
		t.Error("a credential that does not resolve for this user fails closed")
	}
	if !tempToolNeedsConfirm(shell("secret:quiet_gen"), "alice") {
		t.Error("secret: hands the raw key to the script and always asks")
	}
}

// A clean direct run counts as verified, at the bar test sets. The test report
// told an author to call a gated tool directly once, and that call never
// counted: seven clean runs left the tool "unverified".
func TestACleanDirectRunCountsAsVerified(t *testing.T) {
	prev := ToolVerifyRecorder
	var got []string
	ToolVerifyRecorder = func(_ *ToolSession, name string, passed bool, _ string) {
		got = append(got, fmt.Sprintf("%s=%v", name, passed))
	}
	defer func() { ToolVerifyRecorder = prev }()
	sess := newTestSession()
	shell := &TempTool{Name: "s", ScriptBody: "print(1)"}
	api := &TempTool{Name: "a", Mode: TempToolModeAPI, CommandTemplate: "https://x.example/a"}
	piped := &TempTool{Name: "p", Mode: TempToolModeAPI, CommandTemplate: "https://x.example/p", ResponsePipe: "jq ."}

	recordCleanRun(sess, shell, "done", nil)
	recordCleanRun(sess, shell, "boom\n[exit: exit status 1]", nil)
	recordCleanRun(sess, shell, "", fmt.Errorf("refused"))
	recordCleanRun(sess, api, "HTTP 200 OK\n{}", nil)
	recordCleanRun(sess, api, "HTTP 500 Internal Server Error\n{}", nil)
	recordCleanRun(sess, piped, "{}", nil)
	if strings.Join(got, ",") != "s=true,a=true" {
		t.Errorf("only the clean shell run and the 2xx api call count, got %v", got)
	}
}

// timeout_sec lengthens a shell tool's run, and never shortens it.
func TestAShellToolsTimeoutLengthensItsRun(t *testing.T) {
	if got := shellRunTimeout(&TempTool{TimeoutSec: 200}); got != 200*time.Second {
		t.Errorf("a longer own timeout applies, got %s", got)
	}
	if got := shellRunTimeout(&TempTool{TimeoutSec: 10}); got != commandTimeout {
		t.Errorf("a shorter one does not cut the general cap, got %s", got)
	}
}
