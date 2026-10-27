package admin

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The value↔spec mapping (ReadValues/BuildSpec) now lives in core with the
// templates and is tested there; this covers the admin-only Edit-spec transforms.
func TestNestAndStringifyComfyWorkflow(t *testing.T) {
	// A spec whose comfy_workflow is a string holding pretty JSON.
	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL:     "http://x/prompt",
		ComfyWorkflow: "{\n  \"6\": {\n    \"class_type\": \"CLIPTextEncode\"\n  }\n}",
	})

	// Edit-spec view: comfy_workflow becomes a NESTED object, not an escaped string.
	nested, ok := nestComfyWorkflow(spec)
	if !ok {
		t.Fatal("expected nesting to apply")
	}
	if strings.Contains(string(nested), `\"6\"`) || strings.Contains(string(nested), `\n`) {
		t.Errorf("workflow still escaped in Edit-spec view:\n%s", nested)
	}
	if !strings.Contains(string(nested), "\"comfy_workflow\": {") {
		t.Errorf("comfy_workflow not rendered as a nested object:\n%s", nested)
	}

	// Save path: an edited nested object folds back to a string, content survives.
	backToString := stringifyComfyWorkflow(nested)
	var out RestImageSpec
	if err := json.Unmarshal(backToString, &out); err != nil {
		t.Fatalf("stringified spec invalid: %v\n%s", err, backToString)
	}
	if !strings.Contains(out.ComfyWorkflow, "CLIPTextEncode") {
		t.Errorf("workflow content lost on round-trip: %q", out.ComfyWorkflow)
	}
	if !strings.Contains(out.ComfyWorkflow, "\n") {
		t.Errorf("workflow should be pretty (multi-line) after round-trip: %q", out.ComfyWorkflow)
	}

	// A non-image spec (no comfy_workflow) is passed through untouched.
	if _, ok := nestComfyWorkflow([]byte(`{"credential":"no_auth"}`)); ok {
		t.Error("nesting should not apply to a spec without comfy_workflow")
	}
}
