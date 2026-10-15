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
	app1 := labels("App")
	for _, want := range []string{"File store: Intake (commands: Extract)", "File store: Analysis", "Tool: get_weather", "Agent: Investigator, senior"} {
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
		if f.Name == "build_on" && f.Type == "checklist" && strings.Contains(f.OptionsFrom, "source=build_on&kind={kind}") {
			found = true
		}
	}
	if !found {
		t.Error("Builder's intake form does not ask what to build on")
	}
}
