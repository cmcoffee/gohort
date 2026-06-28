package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestBuildAppPage_FormAndTable verifies the declarative-spec → ui.Page → JSON
// path produces a renderable page with the expected component types and the
// good-defaults wiring (modal create, deletable table keyed on the record key).
func TestBuildAppPage_FormAndTable(t *testing.T) {
	spec := AppSpec{Slug: "reading-list", Name: "Reading List", RecordKey: "id"}
	sections := []any{
		map[string]any{
			"kind":         "form",
			"title":        "Add a book",
			"submit_label": "Add book",
			"modal":        true,
			"fields": []any{
				map[string]any{"field": "title", "label": "Title", "type": "text"},
				map[string]any{"field": "notes", "label": "Notes", "type": "textarea", "rows": float64(3)},
			},
		},
		map[string]any{
			"kind":            "table",
			"title":           "Books",
			"empty_text":      "No books yet.",
			"deletable":       true,
			"auto_refresh_ms": float64(2000),
			"columns": []any{
				map[string]any{"field": "title", "flex": float64(1)},
				map[string]any{"field": "notes", "flex": float64(2), "mute": true},
			},
		},
	}
	page, err := buildAppPage(spec, sections)
	if err != nil {
		t.Fatalf("buildAppPage: %v", err)
	}
	if len(page.Sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(page.Sections))
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	js := string(blob)
	// Component types present.
	for _, want := range []string{`"modal_button"`, `"form_panel"`, `"table"`} {
		if !strings.Contains(js, want) {
			t.Errorf("rendered page missing %s\n%s", want, js)
		}
	}
	// The delete row-action must template the configured record key, and the
	// table sources the per-app records endpoint.
	for _, want := range []string{`record?id={id}`, `"source":"records"`, `"No books yet."`} {
		if !strings.Contains(js, want) {
			t.Errorf("rendered page missing %q\n%s", want, js)
		}
	}
	// Sanity: it's valid JSON.
	var sink any
	if err := json.Unmarshal(blob, &sink); err != nil {
		t.Fatalf("page config is not valid JSON: %v", err)
	}
}

// TestBuildAppPage_Workbench verifies a workbench section produces the full-page
// three-column layout (one no-chrome WorkbenchPanel, full width) with the chat
// + new-button sub-components and the viewer wired to the records store.
func TestBuildAppPage_Workbench(t *testing.T) {
	spec := AppSpec{Slug: "guides", Name: "Guides", RecordKey: "id", AgentID: "agent-x"}
	sections := []any{
		map[string]any{
			"kind":        "workbench",
			"item_label":  "title",
			"body_field":  "content",
			"empty_title": "No guide selected",
		},
	}
	page, err := buildAppPage(spec, sections)
	if err != nil {
		t.Fatalf("buildAppPage workbench: %v", err)
	}
	if page.MaxWidth != "100%" {
		t.Errorf("workbench page should be full width, got %q", page.MaxWidth)
	}
	if len(page.Sections) != 1 || !page.Sections[0].NoChrome {
		t.Fatalf("workbench should be one no-chrome section, got %+v", page.Sections)
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	js := string(blob)
	for _, want := range []string{`"workbench_panel"`, `"agent_loop_panel"`, `"lock_activity":true`, `"modal_button"`, `"record?id={id}"`, `"No guide selected"`} {
		if !strings.Contains(js, want) {
			t.Errorf("workbench page missing %s\n%s", want, js)
		}
	}
}

// A workbench with no agent_id must be refused — the chat column needs an agent.
func TestBuildAppPage_WorkbenchNeedsAgent(t *testing.T) {
	spec := AppSpec{Slug: "g", Name: "G", RecordKey: "id"} // no AgentID
	_, err := buildAppPage(spec, []any{map[string]any{"kind": "workbench"}})
	if err == nil || !strings.Contains(err.Error(), "agent_id") {
		t.Errorf("workbench without agent_id should error on agent_id, got %v", err)
	}
}

func TestBuildAppPage_Errors(t *testing.T) {
	spec := AppSpec{Slug: "x", Name: "X", RecordKey: "id"}
	if _, err := buildAppPage(spec, []any{}); err == nil {
		t.Error("empty sections should error")
	}
	// Unknown kind.
	_, err := buildAppPage(spec, []any{map[string]any{"kind": "bogus"}})
	if err == nil || !strings.Contains(err.Error(), "unknown section kind") {
		t.Errorf("want unknown-kind error, got %v", err)
	}
	// Form with no fields.
	_, err = buildAppPage(spec, []any{map[string]any{"kind": "form"}})
	if err == nil {
		t.Error("form with no fields should error")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Reading List": "reading-list",
		"  My App!! ":  "my-app",
		"Tasks/2026":   "tasks-2026",
		"already-good": "already-good",
		"###":          "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
