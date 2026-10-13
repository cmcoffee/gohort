package servitor

// An agent reaches one of its owner's systems by having it among its Sources,
// chosen on its own page. Servitor's connect screen writes there too, and the
// connections made before this move there once.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeSources stands in for the agent runtime's hooks: owner/agent -> refs.
func fakeSources(t *testing.T) map[string][]ReferenceSelection {
	t.Helper()
	store := map[string][]ReferenceSelection{}
	prevFor, prevSet := AgentSourcesFor, SetAgentSource
	AgentSourcesFor = func(owner, agentID string) []ReferenceSelection { return store[owner+"/"+agentID] }
	SetAgentSource = func(owner, agentID string, sel ReferenceSelection, on bool) error {
		k := owner + "/" + agentID
		kept := []ReferenceSelection{}
		for _, s := range store[k] {
			if s != sel {
				kept = append(kept, s)
			}
		}
		if on {
			kept = append(kept, sel)
		}
		store[k] = kept
		return nil
	}
	t.Cleanup(func() { AgentSourcesFor, SetAgentSource = prevFor, prevSet })
	return store
}

func sourcesApp(t *testing.T) (*Servitor, Database) {
	t.Helper()
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	prev := servitorRef
	servitorRef = app
	t.Cleanup(func() { servitorRef = prev })
	udb := UserDB(app.DB, "alice")
	udb.Set(applianceTable, "lab-box", Appliance{ID: "lab-box", Name: "Lab Box", Type: "ssh", Owner: "alice"})
	return app, udb
}

func TestASystemReachesAnAgentThroughItsSources(t *testing.T) {
	store := fakeSources(t)
	_, udb := sourcesApp(t)

	// A grant alone no longer connects.
	SaveCommandGrant(udb, "wren", "lab-box", nil)
	if applianceEnabledForAgent(udb, "alice", "wren", "lab-box") {
		t.Error("a grant with no Source reached the agent")
	}
	if tools := applianceToolProvider(nil, "alice", "wren"); len(tools) != 0 {
		t.Errorf("tools offered without a Source: %d", len(tools))
	}
	DeleteCommandGrant(udb, "wren", "lab-box")

	// The Source does, and the first time it gets a grant that lets nothing
	// run without asking, rather than the owner's own console settings.
	store["alice/wren"] = []ReferenceSelection{{Kind: "system", ItemID: "lab-box"}}
	if !applianceEnabledForAgent(udb, "alice", "wren", "lab-box") {
		t.Fatal("a Source did not reach the agent")
	}
	if tools := applianceToolProvider(nil, "alice", "wren"); len(tools) == 0 {
		t.Error("no tools offered for a system in the agent's Sources")
	}
	g, ok := loadCommandGrant(udb, "wren", "lab-box")
	if !ok || len(g.Categories) != 0 {
		t.Errorf("a Source should come with an empty grant: %+v ok=%v", g, ok)
	}
}

func TestConnectingFromServitorEditsTheAgentsSources(t *testing.T) {
	store := fakeSources(t)
	app, udb := sourcesApp(t)
	SaveCommandGrant(udb, "wren", "lab-box", nil)
	prevAuth := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "alice", "pw", false)
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prevAuth })

	// Connections made before move to Sources, once.
	app.moveGrantsToSources()
	if got := store["alice/wren"]; len(got) != 1 || got[0].ItemID != "lab-box" {
		t.Fatalf("the old connection is not a Source: %+v", got)
	}
	store["alice/wren"] = nil // the owner removes it
	app.moveGrantsToSources()
	if len(store["alice/wren"]) != 0 {
		t.Error("a removed Source was put back on the next start")
	}
}

// A system shared by name or with a team reaches that person; nobody else.
func TestASystemCanBeSharedWithPeopleAndTeams(t *testing.T) {
	prevAuth := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prevAuth })
	SaveAppGroup(auth, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	t.Cleanup(func() { DeleteAppGroup(auth, "g-ops") })
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Groups: []string{"g-ops"}})
	auth.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	auth.Set(AuthTable, "user:carol", AuthUser{Username: "carol", Groups: []string{"g-ops"}})
	auth.Set(AuthTable, "user:dana", AuthUser{Username: "dana"})

	app, udb := sourcesApp(t)
	a := Appliance{ID: "lab-box", Name: "Lab Box", Type: "ssh", Owner: "alice", AllowedUsers: cleanSystemRecipients([]string{" bob ", "alice", "group:g-ops", "bob"}, "alice")}
	if strings.Join(a.AllowedUsers, ",") != "bob,group:g-ops" {
		t.Fatalf("recipients = %v", a.AllowedUsers)
	}
	udb.Set(applianceTable, a.ID, a)
	app.indexNamedShares("alice", a.ID, a.AllowedUsers)

	for user, want := range map[string]bool{"bob": true, "carol": true, "dana": false} {
		if _, ok := app.systemsSharedWith(user)["lab-box"]; ok != want {
			t.Errorf("%s reaches it = %v, want %v", user, ok, want)
		}
		if _, owner, _, ok := app.resolveAppliance(user, UserDB(app.DB, user), "lab-box"); ok != want || (ok && owner != "alice") {
			t.Errorf("%s resolves it = %v (owner %q), want %v", user, ok, owner, want)
		}
	}
	found := false
	for _, it := range (servitorSource{app: app}).List("carol") {
		if it.ID == "lab-box" {
			found = true
		}
	}
	if !found {
		t.Error("a teammate cannot pick it as a Source")
	}
	// Taken back: gone.
	app.indexNamedShares("alice", a.ID, nil)
	if _, ok := app.systemsSharedWith("bob")["lab-box"]; ok {
		t.Error("an unshared system still reaches bob")
	}
}

// One way to ask each system: one in the agent's Sources is asked through its
// own investigate_<system>, so ask_system is offered only for a member reached
// through a workspace. And a system marked tools-only gives no open-question
// route as a Source either.
func TestEachSystemHasOneWayToAsk(t *testing.T) {
	store := fakeSources(t)
	app, udb := sourcesApp(t)
	udb.Set(applianceTable, "estate", Appliance{ID: "estate", Name: "Estate", Type: "workspace", Owner: "alice", Members: []string{"db-box"}})
	udb.Set(applianceTable, "db-box", Appliance{ID: "db-box", Name: "DB Box", Type: "ssh", Owner: "alice"})
	udb.Set(applianceTable, "logs", Appliance{ID: "logs", Name: "Logs", Type: "command", Owner: "alice", ToolsOnly: true})

	names := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range applianceToolProvider(nil, "alice", "wren") {
			out[d.Tool.Name] = true
		}
		return out
	}
	store["alice/wren"] = []ReferenceSelection{{Kind: "system", ItemID: "lab-box"}}
	if got := names(); got["ask_system"] || !got["request_capability"] {
		t.Errorf("a system in Sources should be asked through its own tool, not ask_system: %v", got)
	}
	store["alice/wren"] = append(store["alice/wren"], ReferenceSelection{Kind: "system", ItemID: "estate"})
	if got := names(); !got["ask_system"] {
		t.Errorf("a workspace member has no other way to be asked, so ask_system should be offered: %v", got)
	}

	tools := func(id string) map[string]bool {
		out := map[string]bool{}
		for _, d := range (servitorSource{app: app}).ItemToolsWithSession(nil, "alice", id) {
			out[d.Tool.Name] = true
		}
		return out
	}
	if got := tools("lab-box"); !got["investigate_lab_box"] {
		t.Errorf("a system Source should carry its investigate tool: %v", got)
	}
	if got := tools("logs"); got["investigate_logs"] || !got["get_logs_facts"] {
		t.Errorf("a tools-only Source should keep its recorded knowledge and no investigate tool: %v", got)
	}
}
