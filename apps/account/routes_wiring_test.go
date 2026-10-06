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

// A user's own API credential has its key set under Connected accounts. The
// handler routes on kind (an own credential may share a name with a
// deployment one), refuses a blank save rather than reading it as a wipe, and
// leaves the credential's reach to APIs, where its configuration is.
func TestOwnCredentialKeysRouteByKind(t *testing.T) {
	b, err := os.ReadFile("account.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"conns = append(conns, Secure().OwnConnectionsFor(user)...)",
		"if body.Kind == ConnKindOwn {",
		`http.Error(w, "paste a key to save", http.StatusBadRequest)`,
		`http.Error(w, "this credential's reach is set under APIs", http.StatusBadRequest)`,
		"Secure().SetOwnedSecret(user, body.Name, secret)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("connections handler lost %q", want)
		}
	}
	// The own branch must run before the deployment lookup, or a name both
	// kinds share would set the deployment credential's key instead.
	own := strings.Index(src, "if body.Kind == ConnKindOwn {")
	dep := strings.Index(src, "c, found := Secure().Load(body.Name)")
	if own < 0 || dep < 0 || own > dep {
		t.Error("the own-credential branch must come before the deployment lookup")
	}
}
