package orchestrate

import (
	"strings"
	"testing"
)

func TestToolTemplateToolBuilds(t *testing.T) {
	gt := toolTemplateTool()
	if gt.Name() != "tool_template" {
		t.Errorf("name = %q, want tool_template", gt.Name())
	}
}

func TestToolTemplateList(t *testing.T) {
	// list needs no session — it enumerates the registered tool templates.
	out, err := toolTemplateList(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rest_call") || !strings.Contains(out, "github_issue") {
		t.Errorf("list is missing the shipped tool templates:\n%s", out)
	}
	// Fields are surfaced so the model knows what options to fill.
	if !strings.Contains(out, "credential") || !strings.Contains(out, "url") {
		t.Errorf("template fields not listed:\n%s", out)
	}
}
