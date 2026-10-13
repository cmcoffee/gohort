package orchestrate

// Changes to an existing bridge, connector, or another family's schedule ask
// the owner, like tools, apps, skills, pipelines, machines and agents; what a
// session made is its own, and an agent managing its own family's schedules
// asks nothing.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// askLog is an ApproveChange that records every question and answers with
// refuse (refusing anything that existed).
type askLog struct {
	asked []string // kind:name for an existing item
	made  []string // kind:name marked as the session's own
}

func (l *askLog) approve(kind, name, detail string, existed bool) string {
	if !existed {
		l.made = append(l.made, kind+":"+name)
		return ""
	}
	l.asked = append(l.asked, kind+":"+name+" "+detail)
	return "NOT DONE: declined"
}

func TestBridgeChangesAskTheOwner(t *testing.T) {
	familyFixture(t)
	log := &askLog{}
	sess := &ToolSession{Username: "alice", ApproveChange: log.approve}
	SaveEventMonitor(RootDB, EventMonitor{Owner: "alice", Name: "price-watch", Kind: EventKindWatch, SourceKind: "url", WakeAgent: "a-writer"})

	tool := bridgeDefTool("").(*GroupedTool)
	run := func(args map[string]any) string {
		out, err := tool.RunWithSession(args, sess)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out
	}
	if out := run(map[string]any{"action": "delete", "name": "price-watch"}); !strings.Contains(out, "NOT DONE") {
		t.Errorf("deleting an existing bridge did not ask: %s", out)
	}
	if _, ok := GetEventMonitor(RootDB, "alice", "price-watch"); !ok {
		t.Error("a declined delete removed the bridge")
	}
	if out := run(map[string]any{"action": "update", "name": "price-watch", "channel": ""}); !strings.Contains(out, "NOT DONE") {
		t.Errorf("re-targeting an existing bridge did not ask: %s", out)
	}
	if len(log.asked) != 2 || !strings.Contains(log.asked[0], "bridge:price-watch Delete it") || !strings.Contains(log.asked[1], "Detach it") {
		t.Errorf("questions = %q", log.asked)
	}
}

func TestConnectorChangesAskTheOwner(t *testing.T) {
	familyFixture(t)
	spec, _ := json.Marshal(map[string]any{"command": "/bin/echo", "args": []string{"hi"}})
	if err := SaveConnector(RootDB, Connector{Name: "echoer", Kind: DesktopCommandConnectorKind, Owner: "alice", Spec: spec}); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	log := &askLog{}
	sess := &ToolSession{Username: "alice", ApproveChange: log.approve}
	if out, err := connectorUpdate(map[string]any{"name": "echoer", "description": "says hi"}, sess); err != nil || !strings.Contains(out, "NOT DONE") {
		t.Errorf("changing an existing connector did not ask: %q %v", out, err)
	}
	if c, _ := GetConnector(RootDB, "echoer"); c.Desc == "says hi" {
		t.Error("a declined update changed the connector")
	}
	if out, err := connectorDelete(map[string]any{"name": "echoer"}, sess); err != nil || !strings.Contains(out, "NOT DONE") {
		t.Errorf("deleting an existing connector did not ask: %q %v", out, err)
	}
	if _, ok := GetConnector(RootDB, "echoer"); !ok {
		t.Error("a declined delete removed the connector")
	}
	if len(log.asked) != 2 || !strings.Contains(log.asked[0], "connector:echoer Change its description") {
		t.Errorf("questions = %q", log.asked)
	}
	// Made in the session: marked as its own.
	made := connectorMadeHere(func(map[string]any, *ToolSession) (string, error) { return "ok", nil })
	if _, err := made(map[string]any{"name": "fresh"}, sess); err != nil || len(log.made) != 1 || log.made[0] != "connector:fresh" {
		t.Errorf("a connector made here was not marked as the session's own: %q %v", log.made, err)
	}
}

func TestScheduleChangesAskOnlyOutsideTheFamily(t *testing.T) {
	familyFixture(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "Nightly pass", AgentID: "a-nit", Mission: "m", IntervalSeconds: 3600})
	log := &askLog{}
	sess := &ToolSession{Username: "alice", ApproveChange: log.approve}

	for _, actor := range []string{"a-writer", "a-critic", "a-nit"} {
		if msg := scheduleChangeApproval(sess, "alice", actor, "pause", "Nightly pass", nil); msg != "" {
			t.Errorf("%s pausing its own family's schedule asked: %s", actor, msg)
		}
	}
	if msg := scheduleChangeApproval(sess, "alice", "seed-builder", "delete", "Nightly pass", nil); !strings.Contains(msg, "NOT DONE") {
		t.Errorf("Builder deleting another agent's schedule did not ask: %q", msg)
	}
	if msg := scheduleChangeApproval(sess, "alice", "seed-builder", "run_now", "Nightly pass", nil); msg != "" {
		t.Errorf("running a schedule now is not a change: %q", msg)
	}
	// Another agent's recurring task, named by the agent it runs on.
	if msg := scheduleChangeApproval(sess, "alice", "seed-builder", "pause", "daily", map[string]any{"agent": "Critic"}); !strings.Contains(msg, "NOT DONE") {
		t.Errorf("pausing another agent's recurring task did not ask: %q", msg)
	}
	// Its own recurring task, by name alone.
	if msg := scheduleChangeApproval(sess, "alice", "a-writer", "delete", "my-own-task", nil); msg != "" {
		t.Errorf("an agent deleting its own recurring task asked: %q", msg)
	}
	if len(log.asked) != 2 || !strings.HasPrefix(log.asked[0], "schedule:Nightly pass Delete it") {
		t.Errorf("questions = %q", log.asked)
	}
}
