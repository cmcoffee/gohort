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

	app := sharingTestApp(t)
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

	w := appAs(t, app, spec, "u", httptest.NewRequest("GET", "/apps/swing/_settings/agent", nil))
	if !strings.Contains(w.Body.String(), `"agent":""`) {
		t.Errorf("a gone agent is still offered as the current one: %s", w.Body.String())
	}
	w = appAs(t, app, spec, "u", httptest.NewRequest("POST", "/apps/swing/_settings/agent", strings.NewReader(`{"agent":"nobody"}`)))
	if w.Code != 400 {
		t.Errorf("bound to something that is not an agent: %d", w.Code)
	}
	w = appAs(t, app, spec, "someone", httptest.NewRequest("GET", "/apps/swing/_settings/agent", nil))
	if w.Code != 403 {
		t.Errorf("someone other than the owner reached the agent setting: %d", w.Code)
	}
}

// The app's settings are a gear, "Settings..." its tooltip, not a tab.
func TestAnAppsSettingsAreAGear(t *testing.T) {
	page := json.RawMessage(`{"title":"Swing","nav":[{"label":"Records","url":"r"}]}`)
	var m struct {
		Nav []struct{ Label, URL, Icon string } `json:"nav"`
	}
	json.Unmarshal(withNavIcon(page, "settings", "Settings...", "/apps/swing/_settings"), &m)
	if len(m.Nav) != 2 || m.Nav[0].Icon != "" || m.Nav[1] != (struct{ Label, URL, Icon string }{"Settings...", "/apps/swing/_settings", "settings"}) {
		t.Errorf("nav = %+v", m.Nav)
	}
}

// agentsHost is an agent host with two agents of u's: Librarian, part of the
// app shelf, and Helper, u's own.
type agentsHost struct{ *orchestrate.OrchestrateApp }

func (agentsHost) LookupAppAgent(owner, id string) (agenthost.Agent, bool) {
	switch id {
	case "lib":
		return agenthost.Agent{ID: "lib", Name: "Librarian"}, true
	case "help":
		return agenthost.Agent{ID: "help", Name: "Helper"}, true
	}
	return agenthost.Agent{}, false
}

func (agentsHost) AgentOwningApp(owner, id string) string {
	if id == "lib" {
		return "shelf"
	}
	return ""
}

// An app whose agent is its own shows it on Settings as part of the app,
// with no picker (which never offers an app's agent), and Settings will not
// swap it. An app asking one of the person's own agents keeps the picker,
// and is never bound to an app's agent from it.
func TestAnAppsOwnAgentIsNotPickedInSettings(t *testing.T) {
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	o := &orchestrate.OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(agentsHost{o})
	app := sharingTestApp(t)

	own := AppSpec{Slug: "shelf", Name: "Shelf", Owner: "u", AgentID: "lib"}
	page := appAs(t, app, own, "u", httptest.NewRequest("GET", "/apps/shelf/_settings", nil)).Body.String()
	if !strings.Contains(page, "Librarian is this app's own agent") || strings.Contains(page, "agent-options") {
		t.Errorf("the app's own agent on Settings:\n%s", page)
	}
	w := appAs(t, app, own, "u", httptest.NewRequest("POST", "/apps/shelf/_settings/agent", strings.NewReader(`{"agent":"help"}`)))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "changes with the app") {
		t.Errorf("Settings swapped the app's own agent: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadSpec("u", "shelf"); got.AgentID != "lib" {
		t.Errorf("the app's agent is now %q", got.AgentID)
	}

	mine := AppSpec{Slug: "notes", Name: "Notes", Owner: "u", AgentID: "help"}
	page = appAs(t, app, mine, "u", httptest.NewRequest("GET", "/apps/notes/_settings", nil)).Body.String()
	if !strings.Contains(page, "agent-options") || strings.Contains(page, "own agent") {
		t.Errorf("an app asking the person's own agent lost its picker:\n%s", page)
	}
	w = appAs(t, app, mine, "u", httptest.NewRequest("POST", "/apps/notes/_settings/agent", strings.NewReader(`{"agent":"lib"}`)))
	if w.Code != 400 {
		t.Errorf("an app was bound to another app's agent: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadSpec("u", "notes"); got.AgentID != "help" {
		t.Errorf("the app's agent is now %q", got.AgentID)
	}
}
