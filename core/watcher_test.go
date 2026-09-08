package core

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"testing"
)

// TestToolArgsSurviveGobForAnythingJSONCanProduce. kvlite stores records with
// gob, and gob refuses to encode a concrete type inside an interface field
// unless that type is registered. A monitor's ToolArgs, a trigger's ToolArgs
// and a turn's tool trace are all map[string]any filled from an LLM's tool
// call — that is, from decoded JSON — so every shape encoding/json produces
// has to round-trip.
//
// It matters more than a save that fails: DBase.Set hands the error to
// Critical, which exits. An unregistered shape in a tool argument would not
// lose a record, it would take the deployment down.
func TestToolArgsSurviveGobForAnythingJSONCanProduce(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(`{
		"s": "x", "n": 1.5, "b": true, "z": null,
		"nested": {"deep": {"deeper": [1, "two", false, null]}},
		"list": [{"a": 1}, ["b"], "c"]
	}`), &decoded); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(EventMonitor{Name: "m", Owner: "u", ToolArgs: decoded}); err != nil {
		t.Fatalf("a monitor carrying ordinary JSON tool args cannot be saved: %v\n"+
			"register the missing shape in the init() in watcher.go — a save that fails here EXITS the process", err)
	}
	var out EventMonitor
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	nested, _ := out.ToolArgs["nested"].(map[string]any)
	deep, _ := nested["deep"].(map[string]any)
	if got, ok := deep["deeper"].([]any); !ok || len(got) != 4 || got[1] != "two" {
		t.Errorf("a nested argument did not survive the round trip: %#v", out.ToolArgs["nested"])
	}
}
