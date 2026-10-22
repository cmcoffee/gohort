package core

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// orderProbe is a tool that records when it ran and how many of its kind ran
// at once.
type orderProbe struct {
	mu      sync.Mutex
	log     []string
	running int32
	peak    int32
}

func (p *orderProbe) tool(name string, caps []Capability, actionCaps map[string][]Capability) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{Name: name, Description: "d", Caps: caps, ActionCaps: actionCaps,
			Parameters: map[string]ToolParam{"id": {Type: "string"}, "action": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			n := atomic.AddInt32(&p.running, 1)
			for q := atomic.LoadInt32(&p.peak); n > q && !atomic.CompareAndSwapInt32(&p.peak, q, n); q = atomic.LoadInt32(&p.peak) {
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&p.running, -1)
			p.mu.Lock()
			p.log = append(p.log, name+":"+args["id"].(string))
			p.mu.Unlock()
			return "ok", nil
		},
	}
}

func runRound(t *testing.T, tools []AgentToolDef, calls []ToolCall) {
	t.Helper()
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{ToolCalls: calls}, {Content: "done", Repeat: true}}}}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: tools, MaxRounds: 3}); err != nil {
		t.Fatalf("loop: %v", err)
	}
}

func call(name, id string, args ...string) ToolCall {
	a := map[string]any{"id": id}
	if len(args) == 2 {
		a[args[0]] = args[1]
	}
	return ToolCall{ID: name + id, Name: name, Args: a}
}

// A round that changes something runs in the order asked, so the call meant
// to see a write sees it. Observed: Builder edited an app's folder and
// published it in one round, and the publish read the folder first.
func TestARoundThatWritesRunsInOrder(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("edit", []Capability{CapWrite}, nil), p.tool("publish", []Capability{CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("edit", "1"), call("publish", "2"), call("publish", "3")})
	if got := p.log; len(got) != 3 || got[0] != "edit:1" || got[1] != "publish:2" || got[2] != "publish:3" {
		t.Errorf("a writing round must run in the order asked, got %v", got)
	}
	if p.peak != 1 {
		t.Errorf("nothing in a writing round should overlap, peak %d", p.peak)
	}
}

// A round that only reads still fans out.
func TestARoundThatOnlyReadsRunsTogether(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("search", []Capability{CapNetwork, CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("search", "1"), call("search", "2"), call("search", "3")})
	if p.peak < 2 {
		t.Errorf("reads should run together, peak %d", p.peak)
	}
}

// A tool that declares nothing has promised nothing, so it orders the round:
// the cost of guessing wrong that way is a slower round, and the cost of the
// other guess is the race.
func TestAnUnannotatedCallOrdersTheRound(t *testing.T) {
	p := &orderProbe{}
	tools := []AgentToolDef{p.tool("mystery", nil, nil), p.tool("search", []Capability{CapRead}, nil)}
	runRound(t, tools, []ToolCall{call("search", "1"), call("mystery", "2"), call("search", "3")})
	if p.peak != 1 {
		t.Errorf("an unannotated call must order the round, peak %d", p.peak)
	}
}

// One action of a grouped tool writing orders the round; its read actions on
// their own do not.
func TestAGroupedToolOrdersByTheActionCalled(t *testing.T) {
	ac := map[string][]Capability{"cat": {CapRead}, "write": {CapWrite}}
	p := &orderProbe{}
	runRound(t, []AgentToolDef{p.tool("workspace", []Capability{CapRead, CapWrite}, ac)},
		[]ToolCall{call("workspace", "1", "action", "cat"), call("workspace", "2", "action", "cat")})
	if p.peak < 2 {
		t.Errorf("two reads through a grouped tool should run together, peak %d", p.peak)
	}
	q := &orderProbe{}
	runRound(t, []AgentToolDef{q.tool("workspace", []Capability{CapRead, CapWrite}, ac)},
		[]ToolCall{call("workspace", "1", "action", "write"), call("workspace", "2", "action", "cat")})
	if q.peak != 1 || len(q.log) != 2 || q.log[0] != "workspace:1" {
		t.Errorf("a write action must order the round, peak %d log %v", q.peak, q.log)
	}
}

// A tool with its own BatchLane keeps its lanes in a writing round: it has
// already said which of its calls may overlap.
func TestOwnLanesSurviveAWritingRound(t *testing.T) {
	p := &orderProbe{}
	var n atomic.Int64
	probe := p.tool("probe", nil, nil)
	probe.BatchLane = func(map[string]any) string { return strconv.FormatInt(n.Add(1)%3, 10) }
	tools := []AgentToolDef{probe, p.tool("note", []Capability{CapWrite}, nil)}
	runRound(t, tools, []ToolCall{call("note", "1"), call("probe", "a"), call("probe", "b"), call("probe", "c")})
	if p.peak < 3 {
		t.Errorf("a tool's own lanes should still run together, peak %d", p.peak)
	}
}
