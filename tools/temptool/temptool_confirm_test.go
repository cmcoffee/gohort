package temptool

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
