package servitor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A map's investigator runs a round's probes side by side, never more than
// mapProbeWidth at once, while its other tools (the plan, the findings) keep
// the order they were asked in. Driven through the real loop, with the same
// lanes mapInvestigate sets, so it is the loop's partitioning that is tested
// and not a copy of it.
func TestMapProbesRunSideBySideAndThePlanKeepsItsOrder(t *testing.T) {
	var running, peak int32
	probe := AgentToolDef{
		Tool: Tool{Name: "probe", Description: "d", Parameters: map[string]ToolParam{"task": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return "found", nil
		},
		BatchLane: mapProbeLane(),
	}
	var mu sync.Mutex
	var order []string
	step := func(name string) AgentToolDef {
		return AgentToolDef{
			Tool: Tool{Name: name, Description: "d", Parameters: map[string]ToolParam{"step": {Type: "string"}}},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				order = append(order, name+":"+args["step"].(string))
				mu.Unlock()
				return "ok", nil
			},
			SerialFirePerBatch: true,
		}
	}
	var calls []ToolCall
	calls = append(calls, ToolCall{ID: "f1", Name: "record_step_findings", Args: map[string]any{"step": "1"}})
	calls = append(calls, ToolCall{ID: "s2", Name: "mark_step_in_progress", Args: map[string]any{"step": "2"}})
	for i, task := range []string{"nginx", "mysql", "logs", "cron", "certs", "users"} {
		calls = append(calls, ToolCall{ID: "p" + string(rune('a'+i)), Name: "probe", Args: map[string]any{"task": task}})
	}
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{ToolCalls: calls}, {Content: "mapped", Repeat: true}}}}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "map it"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{probe, step("record_step_findings"), step("mark_step_in_progress")},
		MaxRounds: 3,
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got := atomic.LoadInt32(&peak); got != mapProbeWidth {
		t.Errorf("at most %d probes at once, and with six asked all %d lanes should fill; peak was %d", mapProbeWidth, mapProbeWidth, got)
	}
	if len(order) != 2 || order[0] != "record_step_findings:1" || order[1] != "mark_step_in_progress:2" {
		t.Errorf("the plan tools must keep their order, got %v", order)
	}
}

// Workers that find the same dead connection reconnect once between them: the
// first dials and the rest take what it made.
func TestSideBySideProbesReconnectOnce(t *testing.T) {
	key, pub := newTestKey(t, "")
	host, port, dials := testSSHServer(t, pub)
	a := Appliance{ID: "sys-reconnect", Host: host, Port: port, User: "tester", PrivateKey: key}
	pr := &probeRun{id: "reconnect-test", userID: "reconnect-user", appliance: a, a: &Servitor{}}
	t.Cleanup(func() { dropConn(pr.userID, a.ID) })
	dead, err := acquireConn(pr.userID, a)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	pr.a.conn = dead
	dead.Close()
	before := atomic.LoadInt32(dials)
	var wg sync.WaitGroup
	got := make([]any, 3)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := pr.reconnect(dead)
			if err != nil {
				t.Errorf("reconnect: %v", err)
			}
			got[i] = c
		}(i)
	}
	wg.Wait()
	if n := atomic.LoadInt32(dials) - before; n != 1 {
		t.Errorf("three workers on one dead connection dialled %d times; once is enough", n)
	}
	if got[0] != got[1] || got[1] != got[2] || got[0] == any(dead) {
		t.Error("every worker must end up on the one new connection")
	}
}
