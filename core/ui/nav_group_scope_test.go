package ui

// Group headings and fleet scope on the orchestrator nav. Both exist for the
// same reason: a menu that sits in one agent's topbar makes every entry in it
// look like that agent's, so an entry about all of them has to say so and has
// to be ASKED that way.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavGroupAndScopeMarshal(t *testing.T) {
	raw, err := json.Marshal(OrchestratorNavItem{
		Label: "Fleet overview", Source: "api/console/fleet",
		Group: "Your fleet", Scope: "fleet", Layout: "cards",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"group":"Your fleet"`, `"scope":"fleet"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s:\n%s", want, raw)
		}
	}
	// An ungrouped item carries neither, so the many menus that want one flat
	// list stay exactly as they were.
	plain, _ := json.Marshal(OrchestratorNavItem{Label: "Runs", Source: "api/runs"})
	for _, unwanted := range []string{"group", "scope"} {
		if strings.Contains(string(plain), unwanted) {
			t.Errorf("%q should be omitted when unset: %s", unwanted, plain)
		}
	}
}

// The rule that matters at runtime: a fleet-scoped source is asked with NO
// agent, and everything else keeps the agent stamp it has always had.
func TestFleetScopedSourceDropsTheAgent(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function orchSourceURL(")
	if i < 0 {
		t.Fatal("orchSourceURL is gone")
	}
	end := strings.Index(src[i:], "\n      }")
	if end < 0 {
		t.Fatal("could not bound orchSourceURL")
	}
	fn := src[i : i+end+len("\n      }")]

	harness := `
global.window = {GOHORT_AGENT_ID: 'agent-7'};
` + fn + `
var perAgent = orchSourceURL('api/console/overview', {label: 'Agent overview'});
if (perAgent !== 'api/console/overview?agent=agent-7') throw new Error('per-agent source lost its agent: ' + perAgent);

var withQuery = orchSourceURL('api/console/runs?limit=5', {});
if (withQuery !== 'api/console/runs?limit=5&agent=agent-7') throw new Error('existing query string mishandled: ' + withQuery);

var fleet = orchSourceURL('api/console/fleet', {scope: 'fleet'});
if (fleet !== 'api/console/fleet') throw new Error('a fleet source must carry no agent, got: ' + fleet);

// No item at all behaves as it always did — every existing caller passes one,
// but the default must stay the agent stamp rather than silently widening.
var legacy = orchSourceURL('api/console/runs');
if (legacy !== 'api/console/runs?agent=agent-7') throw new Error('the default changed: ' + legacy);

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "scope.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("fleet scoping does not hold:\n%s", out)
	}
}
