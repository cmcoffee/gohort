package core

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestToolProvenanceRoundTrip(t *testing.T) {
	// A tool authored from a template carries its provenance. The export/import
	// path marshals the whole TempTool, so a JSON round-trip must preserve it —
	// and the uniform ref must resolve it back to its registered template.
	tt := TempTool{Name: "make_issue", Template: "github_issue", Mode: TempToolModeAPI, CommandTemplate: "https://x/{id}"}
	raw, err := json.Marshal(tt)
	if err != nil {
		t.Fatal(err)
	}
	var back TempTool
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Template != "github_issue" {
		t.Fatalf("provenance lost in round-trip: %q", back.Template)
	}
	if ref := ToolTemplateRef(back); ref.Target != TargetTool || ref.Name != "github_issue" {
		t.Fatalf("unexpected ref %+v", ref)
	}
	tpl, ok := TemplateForTool(back)
	if !ok {
		t.Fatal("provenance did not resolve to a registered template")
	}
	if tpl.Target != TargetTool || tpl.Name != "github_issue" {
		t.Fatalf("resolved wrong template: %+v", tpl)
	}
}

func TestTemplateRefEmpty(t *testing.T) {
	// A hand-authored artifact (no Template) has no provenance and must not
	// resolve to anything — neither a bare ref nor via TemplateForTool.
	if _, ok := (TemplateRef{Target: TargetTool}).Resolve(); ok {
		t.Fatal("empty-name ref should not resolve")
	}
	if _, ok := TemplateForTool(TempTool{Name: "hand"}); ok {
		t.Fatal("hand-authored tool should have no template")
	}
}

func TestRestToolTemplateUserSpecified(t *testing.T) {
	tpl, ok := GetTemplate(TargetTool, "rest_call")
	if !ok {
		t.Fatal("rest_call tool template not registered")
	}
	art, _, err := tpl.BuildSpec(map[string]any{
		"description": "fetch an item",
		"url":         "https://api.example.com/v1/items/{id}",
		"method":      "get",
		"credential":  "example_api",
	})
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	var tt TempTool
	if err := json.Unmarshal(art, &tt); err != nil {
		t.Fatalf("artifact not a TempTool: %v", err)
	}
	if tt.Mode != TempToolModeAPI || tt.Credential != "example_api" {
		t.Errorf("wrong mode/cred: %+v", tt)
	}
	if tt.Method != "GET" {
		t.Errorf("method not upper-cased: %q", tt.Method)
	}
	if tt.CommandTemplate != "https://api.example.com/v1/items/{id}" {
		t.Errorf("url template lost: %q", tt.CommandTemplate)
	}
	// The {id} placeholder became a required argument.
	if _, ok := tt.Params["id"]; !ok || len(tt.Required) != 1 || tt.Required[0] != "id" {
		t.Errorf("param not derived from placeholder: params=%v required=%v", tt.Params, tt.Required)
	}
}

func TestRestToolTemplateFixedShape(t *testing.T) {
	tpl, ok := GetTemplate(TargetTool, "github_issue")
	if !ok {
		t.Fatal("github_issue template not registered")
	}
	// The user supplies only a credential; url/method/body come from Params.
	art, _, err := tpl.BuildSpec(map[string]any{"credential": "github"})
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	var tt TempTool
	_ = json.Unmarshal(art, &tt)
	if tt.Method != "POST" || tt.CommandTemplate != "https://api.github.com/repos/{owner}/{repo}/issues" {
		t.Errorf("baked request not applied: %+v", tt)
	}
	if tt.Credential != "github" {
		t.Errorf("credential lost: %q", tt.Credential)
	}
	// owner/repo (url) + title/issue_body (body) all derived as arguments.
	got := make([]string, 0, len(tt.Params))
	for k := range tt.Params {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"issue_body", "owner", "repo", "title"}
	if len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("derived params = %v, want %v", got, want)
	}

	// ReadValues round-trips the baked request for the Configure panel.
	vals := tpl.ReadValues(art)
	if vals["method"] != "POST" || vals["credential"] != "github" {
		t.Errorf("ReadValues drift: %v", vals)
	}
}

func TestTemplateTargetsAreSeparate(t *testing.T) {
	// Tool templates list under the tool target, not the connector target.
	toolNames := map[string]bool{}
	for _, tpl := range Templates(TargetTool) {
		toolNames[tpl.Name] = true
		if tpl.Target != TargetTool {
			t.Errorf("template %q listed under tool target but Target=%q", tpl.Name, tpl.Target)
		}
	}
	if !toolNames["rest_call"] || !toolNames["github_issue"] {
		t.Errorf("tool templates missing: %v", toolNames)
	}
	// Connector target must NOT see tool templates, and vice versa.
	if _, ok := GetConnectorTemplate("github_issue"); ok {
		t.Error("tool template leaked into the connector target")
	}
	for _, tpl := range Templates(TargetConnector) {
		if tpl.Name == "rest_call" {
			t.Error("tool template leaked into connector listing")
		}
	}
	// The same name could exist per target without collision (keyed by target).
	if _, ok := GetTemplate(TargetTool, "comfyui"); ok {
		t.Error("connector 'comfyui' should not resolve under the tool target")
	}
}
