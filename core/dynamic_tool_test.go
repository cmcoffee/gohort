package core

// DynamicChatTool lets a tool advertise only what will actually run. The two
// properties that matter are enforced here: the schema must swap in when a
// session is present (and NOT when it isn't, so the global tool index keeps
// seeing a stable string), and it must be byte-identical across builds — a
// description or enum that reorders between turns invalidates the prompt prefix
// cache and re-pays cold prefill on every turn.

import (
	"encoding/json"
	"testing"
)

// dynTool is a DynamicChatTool whose schema is driven by test-set state.
type dynTool struct {
	desc   string
	params map[string]ToolParam
	calls  *int
}

func (d dynTool) Name() string { return "dyn_test_tool" }
func (d dynTool) Desc() string { return "static description" }
func (d dynTool) Params() map[string]ToolParam {
	return map[string]ToolParam{"static": {Type: "string"}}
}
func (d dynTool) Run(map[string]any) (string, error) { return "ok", nil }
func (d dynTool) SchemaWithSession(_ *ToolSession) (string, map[string]ToolParam) {
	if d.calls != nil {
		*d.calls++
	}
	return d.desc, d.params
}

func TestDynamicSchemaReplacesStaticWhenSessionPresent(t *testing.T) {
	tool := dynTool{desc: "live description", params: map[string]ToolParam{"live": {Type: "string"}}}
	def := ChatToolToAgentToolDefWithSession(tool, &ToolSession{})
	if def.Tool.Description != "live description" {
		t.Errorf("description = %q, want the dynamic one", def.Tool.Description)
	}
	if _, ok := def.Tool.Parameters["live"]; !ok {
		t.Errorf("parameters = %v, want the dynamic set", def.Tool.Parameters)
	}
	if _, ok := def.Tool.Parameters["static"]; ok {
		t.Error("static params leaked into the dynamic schema")
	}
}

func TestStaticSchemaWhenNoSession(t *testing.T) {
	// The semantic tool index (tool_index.go) and every session-less picker
	// build without a caller. They must see the stable static shape — a
	// per-session description can't be embedded once and reused.
	calls := 0
	tool := dynTool{desc: "live description", params: map[string]ToolParam{"live": {Type: "string"}}, calls: &calls}
	def := ChatToolToAgentToolDefWithSession(tool, nil)
	if def.Tool.Description != "static description" {
		t.Errorf("description = %q, want the static one", def.Tool.Description)
	}
	if _, ok := def.Tool.Parameters["static"]; !ok {
		t.Errorf("parameters = %v, want the static set", def.Tool.Parameters)
	}
	if calls != 0 {
		t.Errorf("SchemaWithSession ran %d times with a nil session; want 0", calls)
	}
}

func TestChatToolAvailable(t *testing.T) {
	sess := &ToolSession{}

	// Nil params: nothing this caller could use — the catalog drops it.
	if ChatToolAvailable(dynTool{desc: "d", params: nil}, sess) {
		t.Error("a dynamic tool with nil params must report unavailable")
	}
	// Non-nil, even if small: it has something to offer.
	if !ChatToolAvailable(dynTool{desc: "d", params: map[string]ToolParam{"x": {Type: "string"}}}, sess) {
		t.Error("a dynamic tool with params must report available")
	}
	// A nil session has no caller to resolve against, so nothing is filtered.
	if !ChatToolAvailable(dynTool{desc: "d", params: nil}, nil) {
		t.Error("a nil session must not filter tools")
	}
	// Static tools never opt into this and are always available.
	if !ChatToolAvailable(&staticProbeTool{}, sess) {
		t.Error("a static tool must always report available")
	}
}

// staticProbeTool implements only ChatTool — no dynamic schema.
type staticProbeTool struct{}

func (staticProbeTool) Name() string                       { return "static_probe_tool" }
func (staticProbeTool) Desc() string                       { return "static" }
func (staticProbeTool) Params() map[string]ToolParam       { return nil }
func (staticProbeTool) Run(map[string]any) (string, error) { return "", nil }

func TestDynamicSchemaIsDeterministic(t *testing.T) {
	// Tool schemas sit at the front of the prompt. Identical state must produce
	// byte-identical output or every turn re-pays cold prefill.
	tool := dynTool{
		desc: "live description",
		params: map[string]ToolParam{
			"action": {Type: "string", Enum: []string{"find", "fetch", "generate"}},
			"query":  {Type: "string", Description: "q"},
			"url":    {Type: "string", Description: "u"},
		},
	}
	first := ChatToolToAgentToolDefWithSession(tool, &ToolSession{})
	for i := 0; i < 20; i++ {
		next := ChatToolToAgentToolDefWithSession(tool, &ToolSession{})
		a, err := json.Marshal(first.Tool)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b, err := json.Marshal(next.Tool)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("schema drifted between builds:\n first: %s\n  next: %s", a, b)
		}
	}
}

func TestEmptyEnumNeverShips(t *testing.T) {
	// One empty Enum takes the whole tool payload down for the turn, disabling
	// every other tool with it (TestNoEmptyEnumValuesInSource catches the
	// literal form; a dynamic schema reaches it at runtime by filtering its
	// last value out). Such a schema must be reported unavailable AND, if a
	// caller builds the def anyway, fall back to the static shape.
	empty := dynTool{desc: "live", params: map[string]ToolParam{
		"action": {Type: "string", Enum: []string{}},
	}}
	if ChatToolAvailable(empty, &ToolSession{}) {
		t.Error("a schema with an empty enum must report unavailable")
	}
	def := ChatToolToAgentToolDefWithSession(empty, &ToolSession{})
	if def.Tool.Description != "static description" {
		t.Errorf("description = %q, want the static fallback", def.Tool.Description)
	}
	for name, p := range def.Tool.Parameters {
		if p.Enum != nil && len(p.Enum) == 0 {
			t.Errorf("param %q shipped an empty enum", name)
		}
	}
}

func TestEmptyEnumCaughtWhenNested(t *testing.T) {
	// Same hazard one level down — an array-of-enum or an object property.
	nested := dynTool{desc: "live", params: map[string]ToolParam{
		"tags": {Type: "array", Items: &ToolParam{Type: "string", Enum: []string{}}},
	}}
	if ChatToolAvailable(nested, &ToolSession{}) {
		t.Error("an empty enum inside Items must report unavailable")
	}
	prop := dynTool{desc: "live", params: map[string]ToolParam{
		"opts": {Type: "object", Properties: map[string]ToolParam{
			"mode": {Type: "string", Enum: []string{}},
		}},
	}}
	if ChatToolAvailable(prop, &ToolSession{}) {
		t.Error("an empty enum inside Properties must report unavailable")
	}
}
