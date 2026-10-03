package core

import (
	"context"
	"testing"
)

// A probed run stops at the first call the watch picks out: that call is
// recorded and never made, the calls before it run, and the run is cancelled
// so nothing after it runs either.
func TestAProbeStopsAtTheCallItWatchesFor(t *testing.T) {
	ctx, probe, cancel := WithToolProbe(context.Background(), func(name string, args map[string]any) bool {
		return name == "machine" && args["action"] == "create"
	})
	defer cancel()
	ran := map[string]int{}
	tool := func(name string) ToolHandlerFunc {
		return func(context.Context, map[string]any) (string, error) { ran[name]++; return "ok", nil }
	}
	if out, _ := safeInvoke(ctx, "machine", tool("machine help"), map[string]any{"action": "help"}); out != "ok" {
		t.Fatalf("a call the probe passes over did not run: %q", out)
	}
	out, err := safeInvoke(ctx, "machine", tool("machine create"), map[string]any{"action": "create", "unattended": true})
	if err != nil || out != probeStoppedText || ran["machine create"] != 0 {
		t.Fatalf("the watched call: %q %v, ran %d", out, err, ran["machine create"])
	}
	if ctx.Err() == nil {
		t.Fatal("the run was not cancelled at the stop")
	}
	if _, err := safeInvoke(ctx, "tool_def", tool("tool_def"), nil); err == nil || ran["tool_def"] != 0 {
		t.Fatal("a call after the stop ran")
	}
	hit, ok := probe.Hit()
	if !ok || hit.Name != "machine" || hit.Args["unattended"] != true {
		t.Fatalf("hit = %+v %v", hit, ok)
	}
	if seen := probe.Seen(); len(seen) != 2 || seen[0].Args["action"] != "help" {
		t.Fatalf("seen = %+v", seen)
	}
	// Without a probe, nothing changes.
	if out, _ := safeInvoke(context.Background(), "machine", tool("plain"), map[string]any{"action": "create"}); out != "ok" || ran["plain"] != 1 {
		t.Fatal("a run with no probe was stopped")
	}
}
