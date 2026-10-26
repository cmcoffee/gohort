package orchestrate

// The console reaches an agent's sub-resources as api/agents/<id>/<action>,
// each a route of its own. A name that is renamed on one side only fails
// quietly: the Memory button's count read /memory after the endpoint had
// become /facts, got a 404, and simply never showed a number.

import (
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestEveryAgentSubRouteThePageCallsIsServed(t *testing.T) {
	page := readFile(t, "assets/web_assets.html")
	calls := regexp.MustCompile(`'api/agents/' *\+ *encodeURIComponent\([a-zA-Z.]+\) *\+ *'/([a-zA-Z0-9_/\-]+)'`).FindAllStringSubmatch(page, -1)
	if len(calls) == 0 {
		t.Fatal("found no agent sub-route calls; the test is reading the wrong thing")
	}
	mux := routeMux(&OrchestrateApp{})
	for _, c := range calls {
		action := c[1]
		if _, pattern := mux.Handler(httptest.NewRequest("GET", "/api/agents/agent-1/"+action, nil)); pattern == "" {
			t.Errorf("the page calls api/agents/<id>/%s, which no route serves", action)
		}
	}
}
