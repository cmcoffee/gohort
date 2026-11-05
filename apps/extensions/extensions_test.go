package extensions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A slow build names each step and its time; one not being timed is a no-op.
func TestPhaseTimesNameEachStep(t *testing.T) {
	pt := startPhases()
	pt.mark("tools")
	pt.mark("render")
	if len(pt.steps) != 2 || !strings.HasPrefix(pt.steps[0], "tools ") || !strings.HasPrefix(pt.steps[1], "render ") {
		t.Errorf("steps = %q", pt.steps)
	}
	var none *phaseTimes
	none.mark("x")
	none.logIfSlow("nothing")
}

// The page answers only at the bare path: a mistyped API path under it is a
// 404, not the whole page, slow to build, with a 200.
func TestThePageAnswersOnlyAtItsOwnPath(t *testing.T) {
	app, req := authedExtensions(t)
	w := httptest.NewRecorder()
	serve(app)(w, req("GET", "/extensions/api/toolz"))
	if w.Code != http.StatusNotFound {
		t.Errorf("a mistyped API path answered %d", w.Code)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/extensions/"))
	if w.Code == http.StatusNotFound {
		t.Error("the page itself answered 404")
	}
}

// A credential moved to another host is a new key as far as approval goes.
func TestACredentialsHostIsItsSchemeAndHost(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"https://api.example.com/v1", "https://API.example.com/v2", true},
		{"https://api.example.com", "https://attacker.example", false},
		{"https://api.example.com", "http://api.example.com", false},
		{"https://api.example.com", "https://api.example.com:8443", false},
	} {
		if got := sameCredentialHost(c.a, c.b); got != c.same {
			t.Errorf("sameCredentialHost(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
