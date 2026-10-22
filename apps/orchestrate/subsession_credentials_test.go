package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A session a turn spawns for a pipeline stage carries the agent's credential
// limits. Without them a fetch_url_<cred> the agent had switched off went
// through from inside a stage. Asserted on the source because the stage runs a
// whole loop.
func TestAPipelineStageKeepsTheAgentsCredentialLimits(t *testing.T) {
	src := readSourceFile(t, "pipeline_tools.go")
	for _, want := range []string{
		"subSess.AgentID = t.agent.ID",
		"subSess.DeniedCredentials = credentialDenySet(t.agent, t.user)",
		"subSess.DirectCredentialRefusal = directCredentialRefusal(t.agent, subSess)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the pipeline stage session lost %q", want)
		}
	}
}

// Another app building a session for an agent gets that agent's limits: what
// the agent switched off in its credential scope.
func TestAgentCredentialDenyReadsTheAgentsScope(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	a, err := saveAgent(UserDB(root, "u"), AgentRecord{Name: "Ops", Owner: "u", OrchestratorPrompt: "p", DisabledCredentials: []string{"payroll_api"}})
	if err != nil {
		t.Fatal(err)
	}
	if deny := app.AgentCredentialDeny("u", a.ID); !deny["payroll_api"] {
		t.Errorf("the agent's switched-off credential must be denied, got %v", deny)
	}
	if deny := app.AgentCredentialDeny("u", "no-such-agent"); len(deny) != 0 {
		t.Errorf("an unknown agent has nothing to deny, got %v", deny)
	}
}
