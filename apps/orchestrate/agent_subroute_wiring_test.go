package orchestrate

// The console reaches an agent's sub-resources as api/agents/<id>/<action>, and
// handleAgentOne dispatches on <action> by name. A name that is renamed on one
// side only fails quietly: the Memory button's count read /memory after the
// endpoint had become /facts, got a 404, and simply never showed a number.

import (
	"regexp"
	"strings"
	"testing"
)

func TestEveryAgentSubRouteThePageCallsIsServed(t *testing.T) {
	page := readFile(t, "assets/web_assets.html")
	one := funcBody(t, readFile(t, "agents_http.go"), "func (T *OrchestrateApp) handleAgentOne")
	calls := regexp.MustCompile(`'api/agents/' *\+ *encodeURIComponent\([a-zA-Z.]+\) *\+ *'/([a-zA-Z0-9_/\-]+)'`).FindAllStringSubmatch(page, -1)
	if len(calls) == 0 {
		t.Fatal("found no agent sub-route calls; the test is reading the wrong thing")
	}
	for _, c := range calls {
		action := c[1]
		if strings.Contains(one, `action == "`+action+`"`) ||
			strings.Contains(one, `strings.HasPrefix(action, "`+action+`/")`) {
			continue
		}
		t.Errorf("the page calls api/agents/<id>/%s, which handleAgentOne does not serve", action)
	}
}
