package servitor

import (
	"context"
	"strconv"
	"strings"
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

// A worker's command counts as a read only when it writes nothing anywhere,
// not even into the run's scratch directory: a write there and a read of it in
// one round would race. Writes, restarts, lookups that would ask, and a call
// with no command all take the ordered lane ("").
func TestAWorkerBatchesOnlyItsReads(t *testing.T) {
	pr := &probeRun{appliance: Appliance{ID: "lanes", Type: "ssh", Host: "box.example.internal"}, scratch: "/tmp/servitor-lanes"}
	tools := pr.workerLanes([]AgentToolDef{
		{Tool: Tool{Name: "run_command"}}, {Tool: Tool{Name: "read_log"}}, {Tool: Tool{Name: "store_fact"}}, {Tool: Tool{Name: "brand_new_tool"}},
	})
	lane := tools[0].BatchLane
	for _, cmd := range []string{"cat /etc/hosts", "ls -la /var/log", "systemctl status nginx", "grep -n listen /etc/nginx/nginx.conf"} {
		if lane(map[string]any{"command": cmd}) == "" {
			t.Errorf("%q only reads and should batch", cmd)
		}
	}
	for _, cmd := range []string{"rm -f /tmp/x", "touch /tmp/servitor-lanes/out", "systemctl restart nginx", "echo hi > /etc/motd", ""} {
		if got := lane(map[string]any{"command": cmd}); got != "" {
			t.Errorf("%q must take the ordered lane, got lane %q", cmd, got)
		}
	}
	if tools[1].BatchLane == nil || tools[1].BatchLane(nil) == "" {
		t.Error("read_log only reads and should batch")
	}
	for _, i := range []int{2, 3} {
		if tools[i].BatchLane != nil || !tools[i].SerialFirePerBatch {
			t.Errorf("%s must keep the ordered lane", tools[i].Tool.Name)
		}
	}
}

// Reads in one round run together, up to workerReadWidth, while a write in the
// same round and the tools that record findings keep their order. Through the
// real loop, with the lanes workerLanes sets.
func TestAWorkersReadsRunTogetherAndItsWritesInOrder(t *testing.T) {
	pr := &probeRun{appliance: Appliance{ID: "lanes", Type: "ssh", Host: "box.example.internal"}}
	var running, peak int32
	var mu sync.Mutex
	var ordered []string
	run := AgentToolDef{
		Tool: Tool{Name: "run_command", Description: "d", Parameters: map[string]ToolParam{"command": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			cmd := args["command"].(string)
			if strings.HasPrefix(cmd, "cat ") {
				n := atomic.AddInt32(&running, 1)
				for p := atomic.LoadInt32(&peak); n > p && !atomic.CompareAndSwapInt32(&peak, p, n); p = atomic.LoadInt32(&peak) {
				}
				time.Sleep(60 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return "contents", nil
			}
			mu.Lock()
			ordered = append(ordered, cmd)
			mu.Unlock()
			return "done", nil
		},
	}
	fact := AgentToolDef{
		Tool: Tool{Name: "store_fact", Description: "d", Parameters: map[string]ToolParam{"key": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			mu.Lock()
			ordered = append(ordered, "fact:"+args["key"].(string))
			mu.Unlock()
			return "ok", nil
		},
	}
	calls := []ToolCall{
		{ID: "w1", Name: "run_command", Args: map[string]any{"command": "rm -f /tmp/stale"}},
		{ID: "f1", Name: "store_fact", Args: map[string]any{"key": "os"}},
	}
	for i := 0; i < 6; i++ {
		calls = append(calls, ToolCall{ID: "r" + strconv.Itoa(i), Name: "run_command", Args: map[string]any{"command": "cat /etc/file" + strconv.Itoa(i)}})
	}
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{{ToolCalls: calls}, {Content: "done", Repeat: true}}}}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "look"}}, AgentLoopConfig{
		Tools: pr.workerLanes([]AgentToolDef{run, fact}), MaxRounds: 3,
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got := atomic.LoadInt32(&peak); got < 2 || got > workerReadWidth {
		t.Errorf("reads should run together, at most %d at once; peak was %d", workerReadWidth, got)
	}
	if strings.Join(ordered, ",") != "rm -f /tmp/stale,fact:os" {
		t.Errorf("the write and the fact must keep their order, got %v", ordered)
	}
}

// One run keeps at most execSlotsPerRun commands open on the system, however
// many workers and batched reads ask at once.
func TestARunCapsTheCommandsItHasOpen(t *testing.T) {
	pr := &probeRun{ctx: context.Background(), appliance: Appliance{ID: "cap", Type: "command"}, a: &Servitor{}}
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < execSlotsPerRun*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := pr.sshExec("sleep 0.2"); err != nil {
				t.Errorf("exec: %v", err)
			}
		}()
	}
	wg.Wait()
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Errorf("%d commands of 0.2s finished in %s: more than %d ran at once", execSlotsPerRun*2, took, execSlotsPerRun)
	}
}
