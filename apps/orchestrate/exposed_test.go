package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// A Hidden app-agent (Servitor Investigator / Guide Author style) must never be
// publicly exposable, even if a stale Exposed flag lingers on a shadow — that
// was the "why is this on my dashboard" leak. But the guard must be scoped so a
// user's own published agent and a deliberately-visible app-agent are untouched.
func TestHiddenAppAgentNotPubliclyExposable(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: "test-hidden-appagent-xyz", Name: "TestHidden", Hidden: true})
	if publiclyExposable(AgentRecord{ID: "test-hidden-appagent-xyz", ShowOnDashboard: true}) {
		t.Fatal("Hidden app-agent must not be publicly exposable even when Exposed=true")
	}

	// A non-app-agent (a user's own agent) with Exposed=true still exposes.
	if !publiclyExposable(AgentRecord{ID: "not-an-app-agent", ShowOnDashboard: true}) {
		t.Fatal("a normal Exposed agent should remain exposable")
	}

	// An app-agent an app deliberately registers non-Hidden can still expose.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: "test-visible-appagent-xyz", Name: "TestVisible", Hidden: false})
	if !publiclyExposable(AgentRecord{ID: "test-visible-appagent-xyz", ShowOnDashboard: true}) {
		t.Fatal("a non-Hidden app-agent with Exposed=true should remain exposable")
	}
}

// The dashboard card for one of the viewer's own agents opens the restricted
// chat, as a published agent's card does: /agents/<id>, which resolves for
// the owner alone. A sub-agent is not offered and does not resolve, and an
// agent's id never shadows a published slug.
func TestAnOwnAgentsCardOpensTheRestrictedChat(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Scout", OrchestratorPrompt: "Find things."})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Helper", OrchestratorPrompt: "Help.", OwnedBy: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	var paths []string
	for _, c := range T.DashboardPinnable(req) {
		paths = append(paths, c.Path)
		if strings.HasPrefix(c.Path, "/orchestrate/") {
			t.Errorf("a card opens the full console: %s", c.Path)
		}
	}
	if !strings.Contains(strings.Join(paths, " "), "/agents/"+a.ID) {
		t.Fatalf("the agent's card: %v", paths)
	}
	got, entry, ok := T.LookupExposedAgent(req, a.ID)
	if !ok || got.ID != a.ID || entry.Owner != user || !T.AgentReachableBy(req, entry.Slug, entry.Owner, got.AllowedUsers, got.Everyone) {
		t.Fatalf("the owner's card did not open the agent: ok=%v entry=%+v", ok, entry)
	}
	if _, _, ok := T.LookupExposedAgent(req, sub.ID); ok {
		t.Error("a sub-agent opened on its own")
	}
	nobody := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, _, ok := T.LookupExposedAgent(nobody, a.ID); ok {
		t.Error("someone not signed in opened the agent")
	}
}

// A card someone already switched on keeps its place: the old console path
// in their dashboard preferences becomes the restricted one, and a session
// link that happens to share the prefix is left alone.
func TestOwnAgentCardsMoveToTheRestrictedChat(t *testing.T) {
	newTestOrchestrate(t)
	pinRootDB(t)
	RenameDashboardCardPaths("alice", func(p string) string { return p }) // no prefs yet: nothing to do
	SetDashboardOrderOf("alice", []string{"/notes", "/orchestrate/?agent=a1", "/orchestrate/?agent=a1&session=s"})
	T := &OrchestrateApp{}
	T.migrateOwnAgentCardPaths()
	got := DashboardOrderOf("alice", []string{"/agents/a1", "/notes", "/orchestrate/?agent=a1&session=s"})
	if strings.Join(got, " ") != "/notes /agents/a1 /orchestrate/?agent=a1&session=s" {
		t.Errorf("after the move the order reads %v", got)
	}
}
