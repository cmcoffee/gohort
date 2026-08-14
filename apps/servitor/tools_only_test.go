package servitor

// ToolsOnly withholds the asking route for one target.
//
// The reason it exists: asking runs the per-appliance investigator, and
// what the investigator learns is recorded against THAT appliance's
// memory scope (note_lesson / record_technique inside runSession). For a
// machine that is exactly right — one box is one subject. For a target
// that stands in for MANY subjects, such as a command appliance whose
// working directory holds many log bundles, one shared scope means a
// technique from last week's incident is in view while reading this
// week's.
//
// The alternative was one appliance per bundle, which isolates correctly
// and reintroduces the per-bundle setup the arrangement exists to avoid.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func toolsOnlyFixture(t *testing.T) Database {
	t.Helper()
	udb := &DBase{Store: kvlite.MemStore()}
	for _, a := range []Appliance{
		// The target that stands in for many subjects.
		{ID: "logs", Name: "Log bundles", Type: "command", Command: "logparse", WorkDir: "/data/logs", ToolsOnly: true},
		// An ordinary machine, where accumulated technique is the point.
		{ID: "box-1", Name: "app-server", Type: "ssh"},
	} {
		udb.Set(applianceTable, a.ID, a)
	}
	return udb
}

func TestToolsOnlyWithholdsTheAskingRoute(t *testing.T) {
	udb := toolsOnlyFixture(t)
	SaveCommandGrant(udb, "agent-1", "logs", nil)
	SaveCommandGrant(udb, "agent-1", "box-1", nil)

	// Connected either way — the flag is about ASKING, not reach.
	if !applianceEnabledForAgent(udb, "agent-1", "logs") {
		t.Fatal("the grant should still connect the agent")
	}
	if applianceAskableForAgent(udb, "agent-1", "logs") {
		t.Error("a ToolsOnly target must not be askable")
	}
	// The ordinary machine is unaffected.
	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Error("ToolsOnly on one target must not affect another")
	}
}

// Filtering the tool's DESCRIPTION is not enforcement. A model that has
// seen the name once — or read it out of a log it was investigating —
// can name it directly, so the gate the handler already calls is where
// the flag has to live.
func TestToolsOnlyIsAGateNotADescription(t *testing.T) {
	udb := toolsOnlyFixture(t)
	SaveCommandGrant(udb, "agent-1", "logs", nil)

	// The same check ask_system's handler performs on the name it was
	// given, rather than on the list it advertised.
	if applianceAskableForAgent(udb, "agent-1", "logs") {
		t.Fatal("naming the target directly walked past the flag")
	}
}

// Reaching a target through a workspace must not grant what a direct
// connection would have withheld, or the flag is bypassed by putting the
// target in a group.
func TestToolsOnlyIsNotBypassedByAWorkspace(t *testing.T) {
	udb := toolsOnlyFixture(t)
	udb.Set(applianceTable, "ws-1", Appliance{
		ID: "ws-1", Name: "Estate", Type: "workspace", Members: []string{"logs", "box-1"},
	})
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	if applianceAskableForAgent(udb, "agent-1", "logs") {
		t.Error("a workspace grant reached a ToolsOnly member")
	}
	// The workspace's other member is still reachable, so the flag
	// narrows rather than breaking the grant.
	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Error("the workspace grant should still reach its ordinary members")
	}
}

// A tool whose only honest answer is "there is nothing you may ask
// about" teaches the model to stop reading its own catalog.
func TestAskSystemIsOmittedWhenNothingIsAskable(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prev := servitorRef
	servitorRef = &Servitor{}
	servitorRef.DB = root
	defer func() { servitorRef = prev }()

	udb := servitorUserDB("owner")
	if udb == nil {
		t.Fatal("no store")
	}
	udb.Set(applianceTable, "logs", Appliance{
		ID: "logs", Name: "Log bundles", Type: "command", Command: "logparse", ToolsOnly: true,
	})
	SaveCommandGrant(udb, "agent-1", "logs", nil)

	var names []string
	for _, td := range applianceToolProvider(nil, "owner", "agent-1") {
		names = append(names, td.Tool.Name)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, "ask_system") {
		t.Errorf("ask_system was offered with nothing askable: %s", joined)
	}
	// request_capability stays: the agent can still ask for an ability on
	// the target, which is the route that ends in an approved tool rather
	// than an investigator run.
	if !strings.Contains(joined, "request_capability") {
		t.Errorf("request_capability should still be offered: %s", joined)
	}
}
