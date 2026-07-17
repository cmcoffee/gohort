package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestToolboxHandlerLiveResolvesAfterRename pins the mid-turn staleness fix:
// an agent-owned toolbox is pinned into the static catalog and skipped by the
// per-round dynamic-tool refresh, so the AgentToolDef built at turn start would
// keep dispatching the frozen snapshot. After a tool_def update renames an
// action on the SESSION record, the already-built handler must reflect the live
// record — dispatch + help resolve against current actions, not the snapshot.
func TestToolboxHandlerLiveResolvesAfterRename(t *testing.T) {
	sess := newTestSession()
	orig := &TempTool{
		Name:       "moltbook",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "post_message", Description: "old post action", URLTemplate: "https://x.test/p", Method: "GET"},
		},
	}
	if err := sess.AppendTempTool(orig); err != nil {
		t.Fatal(err)
	}

	// Handler built at "turn start" from the snapshot.
	def := agentToolFromTemp(sess, orig)

	// Simulate a mid-turn tool_def rename: the session record now carries
	// create_post instead of post_message (RemoveTempTool + AppendTempTool is
	// exactly what createToolboxGrouped does).
	sess.RemoveTempTool("moltbook")
	renamed := &TempTool{
		Name:       "moltbook",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "create_post", Description: "new post action", URLTemplate: "https://x.test/p", Method: "GET"},
		},
	}
	if err := sess.AppendTempTool(renamed); err != nil {
		t.Fatal(err)
	}

	// help must list the renamed action and drop the stale one.
	help, err := def.Handler(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(help, "create_post") {
		t.Errorf("help does not reflect the live record (missing create_post):\n%s", help)
	}
	if strings.Contains(help, "post_message") {
		t.Errorf("help still lists the stale action post_message:\n%s", help)
	}

	// A call to the now-removed action must be rejected as unknown — proof the
	// frozen snapshot is no longer the dispatch source.
	if _, err := def.Handler(map[string]any{"action": "post_message"}); err == nil ||
		!strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected unknown-action error for the removed action, got: %v", err)
	}
}

// TestTempToolCaps pins the capability tier per mode — the value the loop
// cap-gates on and the live-resolve guard compares against.
func TestTempToolCaps(t *testing.T) {
	cases := []struct {
		name string
		tt   *TempTool
		want []Capability
	}{
		{"api plain", &TempTool{Mode: TempToolModeAPI}, []Capability{CapNetwork}},
		{"api with pipe", &TempTool{Mode: TempToolModeAPI, ResponsePipe: "jq ."}, []Capability{CapNetwork, CapExecute}},
		{"shell", &TempTool{Mode: TempToolModeShell}, []Capability{CapExecute}},
		{"default/empty", &TempTool{}, []Capability{CapExecute}},
	}
	for _, c := range cases {
		got := tempToolCaps(c.tt)
		if len(got) != len(c.want) || !capsSubset(c.want, got) || !capsSubset(got, c.want) {
			t.Errorf("%s: tempToolCaps = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLiveResolveCapGuard proves the guard: an api tool that GROWS its cap
// profile mid-turn (gains a response_pipe → CapExecute) is NOT eligible for
// live dispatch under the snapshot's [CapNetwork] gate, so the snapshot runs
// until next turn; a non-cap edit stays eligible.
func TestLiveResolveCapGuard(t *testing.T) {
	snapCaps := tempToolCaps(&TempTool{Mode: TempToolModeAPI}) // [CapNetwork]

	grown := tempToolCaps(&TempTool{Mode: TempToolModeAPI, ResponsePipe: "jq ."})
	if capsSubset(grown, snapCaps) {
		t.Error("cap-growing edit must NOT be live-dispatch eligible under the snapshot gate")
	}

	sameShape := tempToolCaps(&TempTool{Mode: TempToolModeAPI, CommandTemplate: "https://x/new"})
	if !capsSubset(sameShape, snapCaps) {
		t.Error("non-cap edit must stay live-dispatch eligible")
	}
}

// TestToolboxDisabledActionQuarantined verifies a disabled action is dropped
// from the group (not offered, not dispatchable) while the rest keep working.
func TestToolboxDisabledActionQuarantined(t *testing.T) {
	sess := newTestSession()
	tt := &TempTool{
		Name:       "svc",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{Name: "read_thing", Description: "ok", URLTemplate: "https://x.test/r", Method: "GET"},
			{Name: "broken", Description: "quarantined", URLTemplate: "https://x.test/b", Method: "GET", Disabled: true},
		},
	}
	if err := sess.AppendTempTool(tt); err != nil {
		t.Fatal(err)
	}
	def := agentToolFromTemp(sess, tt)

	help, err := def.Handler(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if strings.Contains(help, "broken") {
		t.Errorf("disabled action should not appear in help:\n%s", help)
	}
	if !strings.Contains(help, "read_thing") {
		t.Errorf("live action missing from help:\n%s", help)
	}
	if _, err := def.Handler(map[string]any{"action": "broken"}); err == nil ||
		!strings.Contains(err.Error(), "unknown action") {
		t.Errorf("disabled action should be unroutable, got: %v", err)
	}
}
