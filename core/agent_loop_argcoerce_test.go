package core

import (
	"reflect"
	"testing"
)

// An array or object the model sent as a JSON string is decoded to what the
// schema asks for; anything else is left exactly as it came.
func TestJSONStringArgsAreDecodedToTheirSchemaType(t *testing.T) {
	params := map[string]ToolParam{
		"phases":  {Type: "array"},
		"options": {Type: "object"},
		"note":    {Type: "string"},
		"broken":  {Type: "array"},
		"prose":   {Type: "array"},
	}
	args := map[string]any{
		"phases":  `[{"name": "probe", "resident": true}]`,
		"options": ` {"depth": 2} `,
		"note":    `["stays", "a string"]`,
		"broken":  `[{"name": "probe"`,
		"prose":   "a sentence, not a list",
	}
	fixed := coerceJSONStringArgs(params, args)
	if len(fixed) != 2 {
		t.Errorf("decoded %v, want phases and options", fixed)
	}
	want := []any{map[string]any{"name": "probe", "resident": true}}
	if !reflect.DeepEqual(args["phases"], want) {
		t.Errorf("phases = %#v", args["phases"])
	}
	if m, ok := args["options"].(map[string]any); !ok || m["depth"] != float64(2) {
		t.Errorf("options = %#v", args["options"])
	}
	if args["note"] != `["stays", "a string"]` {
		t.Errorf("a string parameter was decoded: %#v", args["note"])
	}
	if args["broken"] != `[{"name": "probe"` || args["prose"] != "a sentence, not a list" {
		t.Errorf("text that is not the asked-for JSON was changed: %#v / %#v", args["broken"], args["prose"])
	}
}
