package servitor

// A system's map is Large as its owner sets it, or by type: a repository's
// records far more than a machine's. The editor offers the choice.

import (
	"os"
	"strings"
	"testing"
)

func TestASystemsMapSize(t *testing.T) {
	for _, c := range []struct {
		a    Appliance
		want bool
	}{
		{Appliance{Type: "repo"}, true},
		{Appliance{Type: "ssh"}, false},
		{Appliance{Type: "repo", MapSize: "standard"}, false},
		{Appliance{Type: "toolset", MapSize: "large"}, true},
	} {
		if got := c.a.largeMap(); got != c.want {
			t.Errorf("%s with %q: large=%v, want %v", c.a.Type, c.a.MapSize, got, c.want)
		}
	}
	b, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "map_size:       mapSizeIn.value") {
		t.Error("the system editor does not send its map size")
	}
}
