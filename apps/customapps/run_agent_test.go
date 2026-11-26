package customapps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/appreach"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script's run_agent and run_pipeline reach the app's agent and pipeline as
// the person using the app, and draw on the same daily allowance as ask: a
// backend cannot spend around the cap by calling the other way.
func TestRunAgentAndPipelineShareAsksAllowance(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	prevAgent, prevPipe, prevAsk := appAgentRun, appPipelineRun, appAgentAsk
	t.Cleanup(func() { appAgentRun, appPipelineRun, appAgentAsk = prevAgent, prevPipe, prevAsk })
	var got []string
	appAgentRun = func(ctx context.Context, spec AppSpec, caller, agent, prompt, filesDir string) (string, []string, float64, error) {
		got = append(got, "agent "+caller+" "+agent+" "+prompt)
		return "the dice say 17", nil, 0.30, nil
	}
	appPipelineRun = func(ctx context.Context, spec AppSpec, caller, pipeline, input string) (string, float64, error) {
		got = append(got, "pipeline "+caller+" "+input)
		return "verdict", 0.30, nil
	}
	appAgentAsk = func(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error) {
		got = append(got, "ask")
		return "ok", 0.30, nil
	}
	spec := AppSpec{Slug: "game", Owner: "alice", AgentID: "dm", AskDailyUSD: 5, AskUserDailyUSD: 0.80}
	ctx := context.Background()

	if out, _, err := appscript.AppRunAgent(ctx, spec, "bob", "", "attack the drone"); err != nil || out != "the dice say 17" {
		t.Fatalf("run_agent: %q %v", out, err)
	}
	if out, err := appscript.AppRunPipeline(ctx, spec, "bob", "", "a topic"); err != nil || out != "verdict" {
		t.Fatalf("run_pipeline: %q %v", out, err)
	}
	// bob has spent 0.60 of his 0.80; one more call of any kind takes him over.
	appscript.AppAsk(ctx, spec, "bob", "hi", false)
	if _, _, err := appscript.AppRunAgent(ctx, spec, "bob", "", "again"); err == nil || !strings.Contains(err.Error(), "you have spent") {
		t.Fatalf("run_agent past bob's cap: %v", err)
	}
	if strings.Join(got, "|") != "agent bob  attack the drone|pipeline bob a topic|ask" {
		t.Errorf("calls = %q (a refused call must not reach the agent)", got)
	}
	if _, _, err := appscript.AppRunAgent(ctx, spec, "carol", "Quartermaster", strings.Repeat("x", appPromptMaxBytes+1)); err == nil {
		t.Error("an oversized prompt went through")
	}
}

// What the agent made reaches the script as paths in its own folder, a folder
// per run under agent_files, and a run that made nothing leaves none behind.
func TestRunAgentHandsTheScriptWhatItsAgentMade(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	prevAgent := appAgentRun
	t.Cleanup(func() { appAgentRun = prevAgent })
	render := true
	appAgentRun = func(ctx context.Context, spec AppSpec, caller, agent, prompt, filesDir string) (string, []string, float64, error) {
		if !render {
			return "nothing to render", nil, 0, nil
		}
		os.WriteFile(filepath.Join(filesDir, "take.mp3"), []byte("ID3 take"), 0600)
		return "rendered", []string{"take.mp3"}, 0, nil
	}
	// The package's one workspace root (TestMain), under an owner of this
	// test's own.
	spec := AppSpec{Slug: "studio", Owner: "handback-owner", AgentID: "composer"}
	ctx := context.Background()
	ws, _ := EnsureWorkspaceDir(spec.Owner)
	t.Cleanup(func() { os.RemoveAll(filepath.Join(ws, agentFilesFolder)) })
	// A run nobody claimed, from two days ago, is cleared on the way.
	stale := filepath.Join(ws, agentFilesFolder, "studio-old")
	os.MkdirAll(stale, 0700)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(stale, old, old)

	_, files, err := appscript.AppRunAgent(ctx, spec, "bob", "", "render the chorus")
	if err != nil || len(files) != 1 {
		t.Fatalf("run_agent: %v %v", files, err)
	}
	if !strings.HasPrefix(files[0], agentFilesFolder+"/studio-") || !strings.HasSuffix(files[0], "/take.mp3") {
		t.Errorf("path: %s", files[0])
	}
	if b, err := os.ReadFile(filepath.Join(ws, files[0])); err != nil || string(b) != "ID3 take" {
		t.Errorf("the script cannot read it at that path: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale run's folder was kept")
	}
	render = false
	if _, files, _ := appscript.AppRunAgent(ctx, spec, "bob", "", "again"); len(files) != 0 {
		t.Errorf("files from a run that made none: %v", files)
	}
	if runs, _ := os.ReadDir(filepath.Join(ws, agentFilesFolder)); len(runs) != 1 {
		t.Errorf("a run that made nothing left a folder: %d folders", len(runs))
	}
}

// The agent and pipeline a script runs carry the app's rules on their
// context, read from What it reaches per call: for someone else, their name
// and the app's choices; for the owner, no person, the app's keep-outs still.
// A first use is recorded on the app.
func TestRunAgentAndPipelineCarryTheAppsRules(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	prevAgent, prevPipe := appAgentRun, appPipelineRun
	t.Cleanup(func() { appAgentRun, appPipelineRun = prevAgent, prevPipe })
	var seen []appreach.Rules
	appAgentRun = func(ctx context.Context, spec AppSpec, caller, agent, prompt, filesDir string) (string, []string, float64, error) {
		r, _ := appreach.From(ctx)
		seen = append(seen, r)
		return "ok", nil, 0, nil
	}
	appPipelineRun = func(ctx context.Context, spec AppSpec, caller, pipeline, input string) (string, float64, error) {
		r, _ := appreach.From(ctx)
		seen = append(seen, r)
		return "ok", 0, nil
	}
	spec := AppSpec{Slug: "game", Owner: "alice", AgentID: "dm"}
	applocal.UpdateAppCredentials("alice", "game", func(c *applocal.AppCredentials) {
		c.Lend = map[string]string{"wiki": "withhold"}
		c.Off = []string{"mcp:atl"}
	})
	ctx := context.Background()
	appscript.AppRunAgent(ctx, spec, "bob", "", "go")
	appscript.AppRunPipeline(ctx, spec, "bob", "", "go")
	appscript.AppRunAgent(ctx, spec, "alice", "", "go")
	if len(seen) != 3 {
		t.Fatalf("runs seen: %d", len(seen))
	}
	for i, r := range seen[:2] {
		if r.Person != "bob" || r.Choice("wiki") != "withhold" || r.Choice("other") != "read" || !r.IsOff("mcp:atl") || r.ConnectNeeded == nil {
			t.Errorf("run %d for bob: %+v", i, r)
		}
	}
	if r := seen[2]; r.Person != "" || !r.IsOff("mcp:atl") || r.ConnectNeeded != nil {
		t.Errorf("the owner's run: %+v", r)
	}
	seen[0].Used("wiki")
	if _, ok := applocal.LoadAppCredentials("alice", "game").Used["wiki"]; !ok {
		t.Error("a use was not recorded on the app")
	}
}
