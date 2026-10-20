package orchestrate

// Routes is the app's boot: the hooks other apps call into, the per-agent
// tool seams, the migrations, the background passes, and the HTTP routes. No
// test ran it, so a phase left out of it would have gone unnoticed until a
// deployment came up without it.
//
// Booting sets process-wide hooks and starts background loops, which would
// leak into every other test in this package. So the boot runs in a child
// process: the test runs its own binary again, the child boots a fresh app
// and checks one effect of each phase, and the parent asks only whether the
// child passed.

import (
	"net/http"
	"os"
	"os/exec"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/toolcat"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestBootRunsEveryPhase(t *testing.T) {
	if os.Getenv("GOHORT_BOOT_CHILD") == "1" {
		bootAndCheck(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBootRunsEveryPhase$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GOHORT_BOOT_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the booted app failed its checks: %v\n%s", err, out)
	}
}

func bootAndCheck(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	AuthDB = func() Database { return adb }
	RootDB = root
	// A legacy record the migrations rewrite, written as an old binary wrote it.
	UserDB(root, "alice").Set(agentsTable, "a-legacy", AgentRecord{ID: "a-legacy", Name: "Legacy", Owner: "alice", OrchestratorPrompt: "p", Mode: "orchestrator"})
	// Every hook the boot sets starts empty, so finding it set means the boot set it.
	CredentialToolsResolver, AttachToolToAgent, ReapTrialTools, ResolveExternalAgentFn, toolcat.Filer = nil, nil, nil, nil, nil
	countAgentsData := func() int {
		n := 0
		for _, h := range RegisteredUserDataHandlers() {
			if _, ok := h.(*orchUserData); ok {
				n++
			}
		}
		return n
	}
	dataBefore := countAgentsData()
	if len(ArtifactSelectionForTypes(root, "agent")) != 0 {
		t.Fatal("precondition: the agent artifact type is registered before boot")
	}

	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	mux := http.NewServeMux()
	app.SetWebMux(mux, "")
	app.Routes()

	if countAgentsData() != dataBefore+1 {
		t.Error("wireRunners: a deleted account's agents have no handler")
	}
	if len(ArtifactSelectionForTypes(root, "agent")) == 0 {
		t.Error("wireArtifactTypes: agents do not export")
	}
	if CredentialToolsResolver == nil || orchestrateBaseDB != root {
		t.Error("wireSeams: the credential resolver or the agent store is not wired")
	}
	if ReapTrialTools == nil {
		t.Error("wireChannelHooks: the scoped-tool hooks are not wired")
	}
	if AttachToolToAgent == nil {
		t.Error("wireAgentTools: authored tools cannot reach an agent")
	}
	if ResolveExternalAgentFn == nil {
		t.Error("wireExternalHooks: outside surfaces cannot resolve an agent")
	}
	if toolcat.Filer == nil {
		t.Error("startBackgroundPasses: tools are not filed into categories")
	}
	var a AgentRecord
	if UserDB(root, "alice").Get(agentsTable, "a-legacy", &a); a.Mode == "orchestrator" {
		t.Error("runMigrations: the legacy agent was not rewritten")
	}
	var adopted bool
	if root.Get(deployMigrationsTable, "global_tool_adoption_v1", &adopted); !adopted {
		t.Error("runLateMigrations: the global-tool adoption pass did not run")
	}
	for _, path := range []string{"/api/console/agents", "/api/agents"} {
		r, _ := http.NewRequest("GET", "http://example.invalid"+path, nil)
		if _, pattern := mux.Handler(r); pattern != path {
			t.Errorf("%s is not routed (got %q): wireWakeAndConsole / registerHandlers", path, pattern)
		}
	}
}
