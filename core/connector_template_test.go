package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeSpecPreservesUnknown(t *testing.T) {
	// A field a newer gohort added ("future_field") must survive a rebuild.
	existing := json.RawMessage(`{"credential":"no_auth","future_field":{"a":1},"default_width":512}`)
	updates := json.RawMessage(`{"credential":"cred1","default_width":768}`)
	merged := MergeSpec(existing, updates)
	var m map[string]any
	if err := json.Unmarshal(merged, &m); err != nil {
		t.Fatal(err)
	}
	if m["credential"] != "cred1" {
		t.Errorf("update didn't win: %v", m["credential"])
	}
	if m["default_width"] != float64(768) {
		t.Errorf("update didn't win: %v", m["default_width"])
	}
	if _, ok := m["future_field"]; !ok {
		t.Error("unknown future_field was dropped (forward-compat broken)")
	}
}

func TestComfyTemplateBuildAndRead(t *testing.T) {
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	// Create-style: a workflow with no node map → auto-wired.
	spec, _, err := tpl.BuildSpec(map[string]any{
		"base_url":      "http://localhost:8188",
		"workflow":      comfyGraphSD15,
		"prompt_suffix": "crisp",
	})
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	// The built spec is a valid comfyui connector (mapping model).
	if err := (restImageHandler{}).Validate(Connector{Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("built spec fails Validate: %v", err)
	}
	var s RestImageSpec
	_ = json.Unmarshal(spec, &s)
	if s.ComfyMap.OutputNode != "9" || len(s.ComfyMap.PromptNodes) == 0 {
		t.Errorf("auto-wire didn't populate the map: %+v", s.ComfyMap)
	}
	if s.PromptSuffix != "crisp" {
		t.Errorf("suffix lost: %q", s.PromptSuffix)
	}

	// ReadValues → BuildSpec round-trips (manual map path this time).
	vals := tpl.ReadValues(spec)
	if vals["base_url"] != "http://localhost:8188" {
		t.Errorf("base_url not read back: %v", vals["base_url"])
	}
	if vals["output_node"] != "9" {
		t.Errorf("output_node not read back: %v", vals["output_node"])
	}
	spec2, _, err := tpl.BuildSpec(vals)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var s2 RestImageSpec
	_ = json.Unmarshal(spec2, &s2)
	if s2.ComfyMap.OutputNode != "9" || s2.SubmitURL != s.SubmitURL || s2.PromptSuffix != "crisp" {
		t.Errorf("round-trip drift: %+v", s2.ComfyMap)
	}
}

func TestComfyTemplateDetect(t *testing.T) {
	tpl, _ := GetConnectorTemplate("comfyui")
	if !tpl.HasDetect() {
		t.Fatal("comfyui should have Detect")
	}
	out, _, err := tpl.Detect(map[string]any{"workflow": comfyGraphSD15})
	if err != nil {
		t.Fatal(err)
	}
	if out["prompt_nodes"] != "6" || out["output_node"] != "9" {
		t.Errorf("detect map wrong: %v", out)
	}
	if out["default_width"] != 512 {
		t.Errorf("detect size wrong: %v", out["default_width"])
	}
}

func TestA1111TemplateRoundTrip(t *testing.T) {
	tpl, ok := GetConnectorTemplate("a1111")
	if !ok {
		t.Fatal("a1111 template not registered")
	}
	if tpl.HasDetect() {
		t.Error("a1111 should have no Detect")
	}
	spec, _, err := tpl.BuildSpec(map[string]any{
		"base_url": "http://localhost:7860", "default_steps": 25, "prompt_suffix": "photoreal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (restImageHandler{}).Validate(Connector{Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("a1111 spec invalid: %v", err)
	}
	vals := tpl.ReadValues(spec)
	if vals["base_url"] != "http://localhost:7860" {
		t.Errorf("base_url round-trip: %v (SubmitURL suffix not stripped?)", vals["base_url"])
	}
	if vals["default_steps"] != 25 || vals["prompt_suffix"] != "photoreal" {
		t.Errorf("a1111 values lost: %v", vals)
	}
}

func TestTemplateForConnector(t *testing.T) {
	// Inference fallback (no provenance).
	comfy := Connector{Kind: RestImageConnectorKind, Spec: json.RawMessage(`{"comfy_workflow":"{}"}`)}
	if tpl, ok := TemplateForConnector(comfy); !ok || tpl.Name != "comfyui" {
		t.Errorf("comfy connector → %v %v", tpl.Name, ok)
	}
	a1 := Connector{Kind: RestImageConnectorKind, Spec: json.RawMessage(`{"submit_url":"http://x/sdapi/v1/txt2img"}`)}
	if tpl, ok := TemplateForConnector(a1); !ok || tpl.Name != "a1111" {
		t.Errorf("a1111 connector → %v %v", tpl.Name, ok)
	}
	if _, ok := TemplateForConnector(Connector{Kind: "remote_mcp"}); ok {
		t.Error("non-image connector should have no template")
	}
	// Stored provenance WINS over inference: shape says comfyui, Template says a1111.
	tagged := Connector{Kind: RestImageConnectorKind, Template: "a1111", Spec: json.RawMessage(`{"comfy_workflow":"{}"}`)}
	if tpl, ok := TemplateForConnector(tagged); !ok || tpl.Name != "a1111" {
		t.Errorf("stored Template should win: got %v %v", tpl.Name, ok)
	}
}

func TestConnectorProvenanceTravels(t *testing.T) {
	// toPortable carries the template; the portable form round-trips through JSON,
	// so export→import preserves which template can Configure the connector.
	pc := toPortable(Connector{Name: "cx", Kind: RestImageConnectorKind, Template: "comfyui", Spec: json.RawMessage(`{}`)})
	if pc.Template != "comfyui" {
		t.Errorf("toPortable dropped template: %q", pc.Template)
	}
	b, _ := json.Marshal(pc)
	var back PortableConnector
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Template != "comfyui" {
		t.Errorf("template lost in JSON round-trip: %q", back.Template)
	}
}

// TestDeclarationOnlyBackend is the Stage A proof: a NEW preset-shaped backend is
// a pure declaration — it names the shared rest_image_preset strategy and writes
// no code of its own, yet builds + reads back correctly.
func TestDeclarationOnlyBackend(t *testing.T) {
	RegisterConnectorTemplate(ConnectorTemplate{
		Name:     "sdnext_decl_test",
		Label:    "SD.Next (test)",
		Category: "Image generation",
		Kind:     RestImageConnectorKind,
		Strategy: "rest_image_preset",
		Params:   map[string]string{"preset": "a1111"}, // A1111-compatible API
		Fields:   []TemplateField{{Key: "base_url", Type: "text"}, {Key: "default_steps", Type: "number"}},
	})
	tpl, ok := GetConnectorTemplate("sdnext_decl_test")
	if !ok {
		t.Fatal("declaration not registered")
	}
	spec, _, err := tpl.BuildSpec(map[string]any{"base_url": "http://gpu.box:7860", "default_steps": 30})
	if err != nil {
		t.Fatalf("declaration-only BuildSpec failed: %v", err)
	}
	if err := (restImageHandler{}).Validate(Connector{Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("built spec invalid: %v", err)
	}
	vals := tpl.ReadValues(spec)
	if vals["base_url"] != "http://gpu.box:7860" {
		t.Errorf("base_url not recovered via preset-suffix strip: %v", vals["base_url"])
	}
	if vals["default_steps"] != 30 {
		t.Errorf("steps round-trip: %v", vals["default_steps"])
	}
}

func TestConnectorTemplatesListed(t *testing.T) {
	names := map[string]bool{}
	for _, tpl := range ConnectorTemplates() {
		names[tpl.Name] = true
		// A declaration must name a registered strategy (the code half).
		if tpl.Category == "" || tpl.Label == "" || tpl.Kind == "" || tpl.Strategy == "" {
			t.Errorf("template %q missing required fields", tpl.Name)
		}
		if _, ok := GetConnectorStrategy(tpl.Strategy); !ok {
			t.Errorf("template %q references unregistered strategy %q", tpl.Name, tpl.Strategy)
		}
	}
	if !names["comfyui"] || !names["a1111"] {
		t.Errorf("expected comfyui + a1111 registered, got %v", names)
	}
	// Sanity: TemplateCSV parsing feeds the map cleanly.
	if got := TemplateCSV(map[string]any{"x": " 6 , 7 ,,8 "}, "x"); strings.Join(got, ",") != "6,7,8" {
		t.Errorf("TemplateCSV: %v", got)
	}
}
