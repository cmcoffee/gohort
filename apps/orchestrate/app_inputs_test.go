package orchestrate

import "testing"

// An action's and a data source's declared inputs are kept from app_def, so
// the app's agent is asked for exactly those.
func TestAppScriptInputsAreKept(t *testing.T) {
	acts, _ := appActionDefs([]any{map[string]any{"name": "fetch", "script": "print('{}')",
		"inputs": map[string]any{"case": "The case id", " ": "blank"}}})
	if len(acts) != 1 || acts[0].Inputs["case"] != "The case id" || len(acts[0].Inputs) != 1 {
		t.Errorf("action inputs: %+v", acts)
	}
	ds, _ := appDataSources([]any{map[string]any{"name": "cases", "script": "print('[]')",
		"inputs": map[string]any{"case": "Which case"}}})
	if len(ds) != 1 || ds[0].Inputs["case"] != "Which case" {
		t.Errorf("data source inputs: %+v", ds)
	}
	if acts, _ := appActionDefs([]any{map[string]any{"name": "x", "script": "y"}}); acts[0].Inputs != nil {
		t.Error("an action with no inputs declared got some")
	}
}
