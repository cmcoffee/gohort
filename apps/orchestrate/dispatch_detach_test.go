package orchestrate

// Handing work off versus waiting for it. The framework decides detaching from
// duration everywhere else; here it cannot, because the question is not how
// long the sub-agent takes but whether THIS turn's reply depends on its answer.

import (
	"strings"
	"testing"
)

func TestOnlyAHandoffDispatchDetaches(t *testing.T) {
	turn := &chatTurn{}
	p := turn.agentsDispatchPolicy(true)
	if p.Always == nil {
		t.Fatal("the run action must be able to detach")
	}
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"handoff", map[string]any{"action": "run", "await": false}, true},
		{"awaited", map[string]any{"action": "run", "await": true}, false},
		// Omitted must mean waiting: a caller that needed the answer and got
		// "started" instead writes its reply around a hole.
		{"omitted", map[string]any{"action": "run"}, false},
		// Some models emit booleans as strings.
		{"string false", map[string]any{"action": "run", "await": "false"}, true},
		// Only run dispatches; list/get are local reads.
		{"list", map[string]any{"action": "list", "await": false}, false},
		{"get", map[string]any{"action": "get", "await": false}, false},
	}
	for _, c := range cases {
		if got := p.Always(c.args, nil); got != c.want {
			t.Errorf("%s: detach=%v, want %v", c.name, got, c.want)
		}
	}
}

// The read-only variant has no dispatch at all, so it must declare no policy —
// otherwise it claims a background slot for work it cannot do.
func TestTheReadOnlyAgentsToolDeclaresNoPolicy(t *testing.T) {
	if p := (&chatTurn{}).agentsDispatchPolicy(false); p.Tool != "" {
		t.Errorf("read-only agents tool declared policy %q", p.Tool)
	}
}

// Preflight runs the gates only for the calls that would detach — an awaited
// dispatch raises its own errors inline at the right moment already.
func TestPreflightOnlyGatesAHandoff(t *testing.T) {
	p := (&chatTurn{}).agentsDispatchPolicy(true)
	if p.Preflight == nil {
		t.Fatal("a detaching dispatch must be refusable before it detaches")
	}
	if err := p.Preflight(map[string]any{"action": "run", "await": true}, nil); err != nil {
		t.Errorf("an awaited dispatch must not be pre-gated: %v", err)
	}
}

func TestTheAwaitParamIsDescribedForHandoff(t *testing.T) {
	def := (&chatTurn{agent: AgentRecord{ID: "a"}}).agentsGroupedToolDef(true)
	p, ok := def.Tool.Parameters["await"]
	if !ok {
		t.Fatal("the caller needs a way to say it is not waiting")
	}
	for _, want := range []string{"TRUE (the default)", "handing the work off", "messaging conversation prefer false"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("await description missing %q:\n%s", want, p.Description)
		}
	}
	// The read-only variant must not advertise it.
	if _, ok := (&chatTurn{agent: AgentRecord{ID: "a"}}).agentsGroupedToolDef(false).Tool.Parameters["await"]; ok {
		t.Error("a tool that cannot dispatch must not offer to hand work off")
	}
}
