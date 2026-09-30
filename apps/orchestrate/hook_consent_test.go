package orchestrate

import (
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
