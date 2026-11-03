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
