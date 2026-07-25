package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
