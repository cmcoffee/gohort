package temptool

import (
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tool on a credential that asks before writes is put before the confirm
// gate only when it can write: an api tool by its method, a toolbox by any
// action's, a script always.
func TestToolsAskBeforeWritesOnlyWhenTheyCanWrite(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "tracker", Type: SecureCredBearer, BaseURL: "https://tracker.example", ConfirmWrites: true}, "k"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		tt   TempTool
		want bool
	}{
		"api read":       {TempTool{Mode: TempToolModeAPI, Credential: "tracker"}, false},
		"api write":      {TempTool{Mode: TempToolModeAPI, Credential: "tracker", Method: "POST"}, true},
		"toolbox reads":  {TempTool{Mode: TempToolModeToolbox, Credential: "tracker", Actions: []TempToolAction{{Name: "list"}, {Name: "get", Method: "GET"}}}, false},
		"toolbox writes": {TempTool{Mode: TempToolModeToolbox, Credential: "tracker", Actions: []TempToolAction{{Name: "list"}, {Name: "close", Method: "PATCH"}}}, true},
		"script via":     {TempTool{Mode: TempToolModeShell, HookCapabilities: []string{"fetch_via:tracker"}}, true},
	} {
		tt := c.tt
		if got := tempToolNeedsConfirm(&tt, "root"); got != c.want {
			t.Errorf("%s: needs confirm = %v, want %v", name, got, c.want)
		}
	}
}
