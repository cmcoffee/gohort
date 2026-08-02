package core

// The setup form's "what this backend does" picker. It exists so adding a photo
// editor is a dropdown rather than a JSON paste — but the pasted export has to
// stay authoritative, and a saved backend has to survive re-editing.

import (
	"encoding/json"
	"testing"
)

func buildComfy(t *testing.T, vals map[string]any) RestImageSpec {
	t.Helper()
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	if vals["base_url"] == nil {
		vals["base_url"] = "http://localhost:8188"
	}
	raw, _, err := comfyBuildSpec(tpl, vals)
	if err != nil {
		t.Fatalf("comfyBuildSpec: %v", err)
	}
	var s RestImageSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	return s
}

func TestTypePicksTheStartingGraph(t *testing.T) {
	gen := buildComfy(t, map[string]any{"workflow_type": ComfyTypeGenerate})
	if gen.SupportsImageInput() {
		t.Error("a generate backend must not take source photos")
	}
	if len(gen.ComfyMap.PromptNodes) == 0 {
		t.Error("a generate backend needs a prompt node")
	}

	edit := buildComfy(t, map[string]any{"workflow_type": ComfyTypeEdit})
	if !edit.SupportsImageInput() {
		t.Error("an edit backend must take a source photo")
	}
	if edit.MaxImages() != 1 {
		t.Errorf("edit MaxImages = %d, want 1", edit.MaxImages())
	}
	if len(edit.ComfyMap.PromptNodes) == 0 {
		t.Error("an img2img backend still takes a prompt")
	}

	blend := buildComfy(t, map[string]any{"workflow_type": ComfyTypeBlend})
	if !blend.SupportsImageInput() {
		t.Error("a blend backend must take source photos")
	}
	if blend.MaxImages() != 2 {
		t.Errorf("blend MaxImages = %d, want 2", blend.MaxImages())
	}
	if len(blend.ComfyMap.PromptNodes) != 0 {
		t.Error("a blend graph has no text node")
	}
}

func TestEmptyTypeStaysGenerate(t *testing.T) {
	// Every connector authored before the picker existed has no workflow_type.
	// It must keep meaning what it meant.
	s := buildComfy(t, map[string]any{})
	if s.SupportsImageInput() {
		t.Error("a blank type must stay text-to-image, as it was before the picker")
	}
}

func TestPastedWorkflowBeatsTheType(t *testing.T) {
	// A dropdown that could silently replace a pasted graph would be worse than
	// no dropdown.
	s := buildComfy(t, map[string]any{
		"workflow_type": ComfyTypeGenerate, // says generate…
		"workflow":      ComfyBlendDefaultGraph(),
	})
	if !s.SupportsImageInput() {
		t.Error("the pasted graph must win over the type picker")
	}
	if s.MaxImages() != 2 {
		t.Errorf("MaxImages = %d, want the pasted blend graph's 2", s.MaxImages())
	}
}

func TestTypeIsReadBackFromTheWiring(t *testing.T) {
	// On re-edit the panel should show what the backend IS, not what was picked
	// — someone can select "edit" and paste an upscale graph, and the map is
	// what decides which action it serves.
	tpl, _ := GetConnectorTemplate("comfyui")
	for _, tc := range []struct{ picked, want string }{
		{ComfyTypeGenerate, ComfyTypeGenerate},
		{ComfyTypeEdit, ComfyTypeEdit},
		{ComfyTypeBlend, ComfyTypeBlend},
	} {
		spec := buildComfy(t, map[string]any{"workflow_type": tc.picked})
		raw, _ := json.Marshal(spec)
		got := comfyReadValues(tpl, raw)["workflow_type"]
		if got != tc.want {
			t.Errorf("picked %q, read back %v, want %q", tc.picked, got, tc.want)
		}
	}
}

func TestDetectReportsTheType(t *testing.T) {
	tpl, _ := GetConnectorTemplate("comfyui")
	out, _, err := comfyDetect(tpl, map[string]any{"workflow": ComfyBlendDefaultGraph()})
	if err != nil {
		t.Fatalf("comfyDetect: %v", err)
	}
	if out["workflow_type"] != ComfyTypeBlend {
		t.Errorf("detect reported %v, want blend", out["workflow_type"])
	}
	if out["image_nodes"] != JoinCSV([]string{"1", "2"}) {
		t.Errorf("detect image nodes = %v, want both LoadImage ids", out["image_nodes"])
	}
}

func TestEditedImageNodeOrderSurvivesResave(t *testing.T) {
	// Node-id order is arbitrary relative to "the first photo", so a compose
	// backend's order is THE field an admin hand-corrects. Re-saving used to
	// discard it on a blend, because the "has a map" check looked only at
	// prompt_nodes and a blend graph has none.
	tpl, _ := GetConnectorTemplate("comfyui")
	spec := buildComfy(t, map[string]any{"workflow_type": ComfyTypeBlend})
	raw, _ := json.Marshal(spec)
	vals := comfyReadValues(tpl, raw)

	// The admin swaps subject and background.
	vals["image_nodes"] = "2,1"
	resaved, _, err := comfyBuildSpec(tpl, vals)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	var out RestImageSpec
	if err := json.Unmarshal(resaved, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !eqStrs(out.ComfyMap.ImageNodes, []string{"2", "1"}) {
		t.Errorf("image nodes = %v, want the admin's [2 1] — the correction was undone", out.ComfyMap.ImageNodes)
	}
}

func TestTypeOfClassifiesByWiringNotClass(t *testing.T) {
	cases := []struct {
		name string
		m    ComfyNodeMap
		want string
	}{
		{"text only", ComfyNodeMap{PromptNodes: []string{"6"}}, ComfyTypeGenerate},
		{"photo plus text", ComfyNodeMap{PromptNodes: []string{"6"}, ImageNodes: []string{"11"}}, ComfyTypeEdit},
		{"photos, no text", ComfyNodeMap{ImageNodes: []string{"1", "2"}}, ComfyTypeBlend},
		{"nothing wired", ComfyNodeMap{}, ComfyTypeGenerate},
	}
	for _, c := range cases {
		if got := ComfyWorkflowTypeOf(c.m); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStarterGraphFallsBackToGenerate(t *testing.T) {
	for _, bad := range []string{"", "nonsense", "CUSTOM"} {
		if ComfyStarterGraph(bad) != comfyDefaultGraph {
			t.Errorf("ComfyStarterGraph(%q) should fall back to the text-to-image graph", bad)
		}
	}
	if ComfyStarterGraph("EDIT") != comfyEditDefaultGraph {
		t.Error("the type should be case-insensitive")
	}
}

func TestTemplateOffersTheTypeField(t *testing.T) {
	tpl, ok := GetConnectorTemplate("comfyui")
	if !ok {
		t.Fatal("comfyui template not registered")
	}
	var found bool
	for _, f := range tpl.Fields {
		if f.Key != "workflow_type" {
			continue
		}
		found = true
		if f.Type != "select" {
			t.Errorf("workflow_type type = %q, want select", f.Type)
		}
		if len(f.Options) != 3 {
			t.Errorf("options = %v, want all three starting points", f.Options)
		}
		if f.Default != ComfyTypeGenerate {
			t.Errorf("default = %v, want generate", f.Default)
		}
	}
	if !found {
		t.Error("the comfyui template must offer workflow_type")
	}
}
