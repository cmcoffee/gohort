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

// A system-role message goes to systemInstruction, after the configured system
// prompt, and never into contents: Gemini answers a "system" turn with a 400,
// which sent every judge and proposer that briefs by message to the worker.
func TestGeminiSystemMessagesBecomeTheInstruction(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You review builds."},
		{Role: "user", Content: "Here is one."},
	}
	if got := geminiSystemText("Be brief.", msgs); got != "Be brief.\n\nYou review builds." {
		t.Fatalf("system text = %q", got)
	}
	if got := geminiSystemText("", msgs[1:]); got != "" {
		t.Fatalf("no system text should be none, got %q", got)
	}
	for _, c := range (&geminiClient{}).buildMessages(msgs) {
		if c.Role == "system" {
			t.Fatal("a system turn went into contents")
		}
	}
}

// TestParamDefaultInSchema: a param's default reaches the schema the model
// reads, so it can leave the param out knowing what it gets, and a param
// with none carries no "default" key at all.
func TestParamDefaultInSchema(t *testing.T) {
	raw := buildToolParamsSchema(Tool{Parameters: map[string]ToolParam{
		"limit": {Type: "integer", Description: "page size", Default: float64(20)},
		"q":     {Type: "string", Description: "query"},
	}})
	var node struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := node.Properties["limit"]["default"]; got != float64(20) {
		t.Errorf("limit default = %v, want 20; schema %s", got, raw)
	}
	if _, has := node.Properties["q"]["default"]; has {
		t.Errorf("q has no default and must not carry the key; schema %s", raw)
	}
}

// Gemini 3 signs each tool-calling turn and refuses the next request unless
// the signature comes back with the call ("Function call is missing a
// thought_signature"). Every lead round after the first failed that way and
// fell back to the worker. The signature is read off the response and sent
// back; a call with none of its own (a worker round, an old stored turn) gets
// the documented skip value so the request still goes through.
func TestGeminiEchoesThoughtSignature(t *testing.T) {
	var resp gemResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"web_search","args":{"q":"x"}},"thoughtSignature":"SIG1"}
	]}}]}`), &resp); err != nil {
		t.Fatal(err)
	}
	_, _, calls := parseGeminiResponse(resp)
	if len(calls) != 1 || calls[0].Signature != "SIG1" {
		t.Fatalf("signature not read off the response: %+v", calls)
	}

	c := &geminiClient{model: "gemini-3.5-flash"}
	got := c.buildMessages([]Message{
		{Role: "user", Content: "look it up"},
		{Role: "assistant", ToolCalls: calls},
		{Role: "user", ToolResults: []ToolResult{{ID: calls[0].ID, Content: "found"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "w1", Name: "a"}, {ID: "w2", Name: "b"}}},
	})
	if s := got[1].Parts[0].ThoughtSignature; s != "SIG1" {
		t.Errorf("the lead's own signature should go back, got %q", s)
	}
	worker := got[3].Parts
	if worker[0].ThoughtSignature != geminiSkipSignature {
		t.Errorf("an unsigned first call should carry the skip value, got %q", worker[0].ThoughtSignature)
	}
	if worker[1].ThoughtSignature != "" {
		t.Errorf("only the first call of a turn is signed, got %q on the second", worker[1].ThoughtSignature)
	}

	// Older models never signed anything, so nothing is invented for them.
	old := (&geminiClient{model: "gemini-2.5-flash"}).buildMessages([]Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "w1", Name: "a"}}},
	})
	if s := old[1].Parts[0].ThoughtSignature; s != "" {
		t.Errorf("gemini-2.5 got a signature it never sent: %q", s)
	}
}
