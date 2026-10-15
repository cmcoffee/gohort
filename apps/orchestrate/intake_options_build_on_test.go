package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
)

// "What should it build on?" lists, for the kind being built, the stores
// (with their commands), the user's tools and agents; values read as what they
// are and carry no comma, which the form joins picks with.
func TestBuildOnListsWhatAThingCanUse(t *testing.T) {
	prev := appstores.Provider
	appstores.Provider = fakeStoreCatalog{}
	t.Cleanup(func() { appstores.Provider = prev })
	_, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Investigator, senior", OrchestratorPrompt: "Explain failures."}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, user, TempTool{Name: "get_weather", Description: "Weather.", CommandTemplate: "python3 x.py"}); err != nil {
		t.Fatal(err)
	}

	labels := func(kind string) string {
		var out []string
		for _, o := range buildOnOptions(udb, user, kind) {
			if strings.Contains(o.Value, ",") {
				t.Errorf("%s: a value carries a comma: %q", kind, o.Value)
			}
			out = append(out, o.Label)
		}
		return strings.Join(out, "|")
	}
	// Another kind of attachable source (a system, a document space, an MCP
	// server exposed as a source) is offered too.
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	// An MCP server whose tools are offered is listed; one that is off is not.
	if err := MCP().Save(MCPServerConfig{Name: "buildon-tracker", URL: "http://127.0.0.1:1/mcp", Enabled: true, ExposeTools: true}, ""); err != nil {
		t.Fatal(err)
	}
	MCP().Save(MCPServerConfig{Name: "buildon-off", URL: "http://127.0.0.1:1/mcp", ExposeTools: true}, "")
	t.Cleanup(func() { MCP().Delete("buildon-tracker"); MCP().Delete("buildon-off") })
	app1 := labels("App")
	if !strings.Contains(app1, "MCP server: buildon-tracker") || strings.Contains(app1, "buildon-off") {
		t.Errorf("MCP servers: %s", app1)
	}
	for _, want := range []string{"File store: Intake (commands: Extract)", "File store: Analysis", "Tool: get_weather", "Agent: Investigator, senior", "Test files: Prod logs"} {
		if !strings.Contains(app1, want) {
			t.Errorf("App is missing %q: %s", want, app1)
		}
	}
	if tool := labels("Tool"); strings.Contains(tool, "Tool: ") || strings.Contains(tool, "Agent: ") || !strings.Contains(tool, "File store: Intake") {
		t.Errorf("a tool is offered: %s", tool)
	}
	if other := labels("Other"); other != "" {
		t.Errorf("Other is offered: %s", other)
	}
	var builder AgentRecord
	for _, a := range seedAgents() {
		if isBuilderAgent(a.ID) {
			builder = a
		}
	}
	found := false
	for _, f := range builder.IntakeForm {
		if f.Name == "build_on" && f.Type == "checklist" && strings.Contains(f.OptionsFrom, "source=build_on&kind={kind}") && f.ExclusiveOption == "Let Builder work it out" {
			found = true
		}
	}
	if !found {
		t.Error("Builder's intake form does not ask what to build on")
	}
}
