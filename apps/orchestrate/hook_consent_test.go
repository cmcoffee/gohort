package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
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
