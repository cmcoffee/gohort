package customapps

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app whose agent was deleted says so to its owner, on the page and in
// Settings, and Settings will not bind it to something that is not an agent.
func TestAnAppWhoseAgentIsGoneSaysSo(t *testing.T) {
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	o := &orchestrate.OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(o)

	app := &CustomApps{}
	spec := AppSpec{Slug: "swing", Name: "Swing", Owner: "u", AgentID: "a-deleted"}
	if app.appAgentExists(spec) {
		t.Fatal("an agent that is not there reads as existing")
	}
	if app.appAgentExists(AppSpec{Slug: "plain", Owner: "u"}) != true {
		t.Error("an app with no agent reads as missing one")
	}

	page := json.RawMessage(`{"title":"Swing","nav":[]}`)
	var m struct {
		Nav []struct{ Label, URL string } `json:"nav"`
	}
	json.Unmarshal(withNavLink(page, "⚠ Its agent was deleted: pick another", "/apps/swing/_settings"), &m)
	if len(m.Nav) != 1 || !strings.HasSuffix(m.Nav[0].URL, "/_settings") {
		t.Errorf("nav = %+v", m.Nav)
	}

	w := httptest.NewRecorder()
	app.handleSettingsAgent(w, httptest.NewRequest("GET", "/x", nil), spec, true)
	if !strings.Contains(w.Body.String(), `"agent":""`) {
		t.Errorf("a gone agent is still offered as the current one: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleSettingsAgent(w, httptest.NewRequest("POST", "/x", strings.NewReader(`{"agent":"nobody"}`)), spec, true)
	if w.Code != 400 {
		t.Errorf("bound to something that is not an agent: %d", w.Code)
	}
	w = httptest.NewRecorder()
	app.handleSettingsAgent(w, httptest.NewRequest("GET", "/x", nil), spec, false)
	if w.Code != 403 {
		t.Errorf("someone other than the owner reached the agent setting: %d", w.Code)
	}
}
