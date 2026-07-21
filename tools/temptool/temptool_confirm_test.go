package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
		{"api mode", &TempTool{Mode: TempToolModeAPI}, true},
		{"has credential", &TempTool{Credential: "graph"}, true},
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
