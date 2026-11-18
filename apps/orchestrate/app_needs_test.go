package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/apptools"
)

// An imported app lists what it names that only a deployment has: the
// credentials its scripts and tools reach, the tools it calls that are not
// found here, its agents' voices, the stores it was granted. One found here is
// mapped to itself; the rest wait for an answer, which then holds wherever
// the app reaches one: a mapping under the importer's name, a going-without
// refused with a message saying where to map it.
func TestAnImportedAppIsAskedForWhatItNames(t *testing.T) {
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, nil)
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	spec, _ := LoadAppSpec("u", "desk")
	spec.Actions = append(spec.Actions, AppAction{Name: "sync", Script: "print('{}')", Capabilities: []string{"fetch_via:crm", "tool:jira_search", "tool:lookup"}})
	SaveAppSpec(spec)
	apptools.SaveBy("u", "desk", TempTool{Name: "deals", Mode: TempToolModeAPI, Credential: "crm"}, "import")
	clerk, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help.", OwningApp: "desk", Voice: "zz_voice",
		AllowedTools: []string{"jira_search", "lookup"}, appEdit: "Builder"})

	needs := recordAppNeeds("u", "desk", []applocal.AppNeed{{Kind: "store", Name: "captures", Write: true}})
	got := map[string]applocal.AppNeed{}
	for _, n := range needs {
		got[n.Kind+":"+n.Name] = n
	}
	for _, key := range []string{"credential:crm", "tool:jira_search", "voice:zz_voice", "store:captures"} {
		if n, ok := got[key]; !ok || n.Answered() {
			t.Errorf("%s: listed %v, answered %+v", key, ok, n)
		}
	}
	if n := got["tool:lookup"]; n.Map != "lookup" {
		t.Errorf("a tool found here is not mapped to itself: %+v", n)
	}
	if !got["store:captures"].Write {
		t.Error("the store need lost what it asks of the store")
	}

	// Answered: crm is the importer's sales, jira_search goes without.
	for i := range needs {
		switch needs[i].Kind + ":" + needs[i].Name {
		case "credential:crm":
			needs[i].Map = "sales"
		case "tool:jira_search":
			needs[i].Off = true
		case "voice:zz_voice":
			needs[i].Off = true
		}
	}
	applocal.SaveAppNeeds("u", "desk", needs)
	if to, err := applocal.AppCredentialAlias("u", "desk")("crm"); err != nil || to != "sales" {
		t.Errorf("crm means %q (%v)", to, err)
	}
	if _, off := applocal.AppNeedAlias("u", "desk", "tool", "jira_search"); !off {
		t.Error("a tool gone without is not")
	}
	if err := applocal.AppNeedRefusal("u", "desk", "tool", "jira_search"); !strings.Contains(err.Error(), "Connections") {
		t.Errorf("the refusal does not say where to map it: %v", err)
	}
	ct := &chatTurn{user: "u", udb: udb, agent: clerk}
	ct.mapAppAgentTools()
	if strings.Join(ct.agent.AllowedTools, ",") != "lookup" {
		t.Errorf("the app's agent's tools here: %v", ct.agent.AllowedTools)
	}
	if v := agentVoice("u", clerk.ID); v != "" {
		t.Errorf("a voice gone without speaks as %q, want the deployment's", v)
	}
	if again := recordAppNeeds("u", "desk", nil); func() string {
		for _, n := range again {
			if n.Name == "crm" {
				return n.Map
			}
		}
		return ""
	}() != "sales" {
		t.Error("listing again lost the answer given")
	}
}
