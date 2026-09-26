package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeGeminiSchema: every array missing "items" gets a permissive
// string-items default (Gemini rejects arrays without items); arrays that
// already declare items are untouched, and the fix reaches nested params.
func TestSanitizeGeminiSchema(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"hook_capabilities":{"type":"array","description":"strings"},
			"tags":{"type":"array","items":{"type":"string"}},
			"nested":{"type":"object","properties":{
				"steps":{"type":"array","description":"objects"}
			}},
			"name":{"type":"string"}
		}
	}`)
	out := string(sanitizeGeminiSchema(raw))

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("sanitized schema is not valid JSON: %v", err)
	}
	props := m["properties"].(map[string]interface{})
	// Array without items now has items.
	hc := props["hook_capabilities"].(map[string]interface{})
	if _, ok := hc["items"]; !ok {
		t.Error("hook_capabilities array still missing items")
	}
	// Nested array reached too.
	steps := props["nested"].(map[string]interface{})["properties"].(map[string]interface{})["steps"].(map[string]interface{})
	if _, ok := steps["items"]; !ok {
		t.Error("nested steps array still missing items")
	}
	// Pre-existing items preserved (still string).
	tags := props["tags"].(map[string]interface{})["items"].(map[string]interface{})
	if tags["type"] != "string" {
		t.Errorf("existing items clobbered: %v", tags)
	}
	// A non-array param never gains items.
	if _, ok := props["name"].(map[string]interface{})["items"]; ok {
		t.Error("non-array param wrongly got items")
	}
	// No array node lacks items anymore.
	if strings.Contains(out, `"type":"array"`) && !strings.Contains(out, `"items"`) {
		t.Error("an array without items survived")
	}
}

// An empty stored turn in replayed history became a part with no field, and
// Gemini refused the whole request for it, which pushed a lead-pinned run onto
// the worker. Empty turns are dropped, and the turns either side of one merge
// so the request still alternates.
func TestGeminiDropsEmptyTurns(t *testing.T) {
	c := &geminiClient{}
	got := c.buildMessages([]Message{
		{Role: "user", Content: "build the connector"},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "go with option A"},
		{Role: "assistant", Content: "Done."},
	})
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "{}") {
		t.Errorf("an empty part reached the request: %s", raw)
	}
	if len(got) != 2 || got[0].Role != "user" || len(got[0].Parts) != 2 || got[1].Role != "model" {
		t.Fatalf("the empty model turn should drop and the two user turns merge: %s", raw)
	}
}
