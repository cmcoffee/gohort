package account

// The Account page carried two panels whose every call went to an endpoint
// that no longer exists here (api/credentials, api/tools). They had moved to
// another app and the panels were no longer rendered, so nothing failed; they
// were waiting for somebody to put them back on the page and find it 404ing.
// Every relative call this package makes has to land on a route it registers.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestEveryAccountCallIsARegisteredRoute(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"account.go", "artifacts.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src.Write(b)
	}
	all := src.String()
	calls := regexp.MustCompile(`(?:fetch\('|Source: *")(api/[a-zA-Z0-9_\-/]+)`).FindAllStringSubmatch(all, -1)
	if len(calls) == 0 {
		t.Fatal("found no calls; the test is reading the wrong thing")
	}
	for _, c := range calls {
		if !strings.Contains(all, `T.HandleFunc("/`+c[1]+`"`) {
			t.Errorf("the page calls %s, which Account does not register", c[1])
		}
	}
}
