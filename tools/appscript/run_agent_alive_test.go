package appscript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A scheduled action that runs an agent sits silent on the call while the
// agent works. The agent's rounds keep the run alive; before they did, a
// newspaper refresh whose agent took longer than the no-output limit was
// stopped as stuck in the middle of its searches.
func TestAnAgentAtWorkKeepsALongScriptAlive(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-runagent-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, "tune_tool_idle_limit", 0.5)
	db.Set(WebTable, "tune_tool_detach_after", 60.0)
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })

	working := true
	prev := AppRunAgent
	AppRunAgent = func(ctx context.Context, spec AppSpec, caller, agent, prompt string) (string, error) {
		for i := 0; i < 15; i++ { // 1.5s of agent work, three times the limit
			if working {
				bgrun.RunActivityFrom(ctx).Working("news: round " + string(rune('1'+i%9)))
			}
			time.Sleep(100 * time.Millisecond)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
		}
		return "front page ready", nil
	}
	t.Cleanup(func() { AppRunAgent = prev })

	spec := AppSpec{Slug: "paper", Owner: "alice", AgentID: "news"}
	run := func() string {
		out, _ := Job{Owner: "alice", Slug: "paper", Kind: "action", Name: "refresh", Spec: &spec, Long: true, Ctx: context.Background(),
			Caps:   []string{"run_agent"},
			Script: `from gohort import run_agent; import json; print(json.dumps({"message": str(run_agent("today's stories"))}))`,
			Args:   map[string]any{}}.Run()
		return out
	}
	out := run()
	if strings.Contains(out, "sandbox") && strings.Contains(out, "not") && !strings.Contains(out, "front page") {
		t.Skipf("no sandbox here: %s", out)
	}
	if !strings.Contains(out, "front page ready") {
		t.Errorf("a script waiting on a working agent was stopped: %s", out)
	}

	// The limit is real: an agent that shows no sign of work still ends it.
	working = false
	if out := run(); strings.Contains(out, "front page ready") {
		t.Errorf("a script waiting on a silent agent ran past the no-output limit: %s", out)
	}
}
