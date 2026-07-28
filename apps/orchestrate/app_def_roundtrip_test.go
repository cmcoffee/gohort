package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The failure these cover: app_def action=get handed back the RENDERED page
// ({title, body:{type:"card", html}}), which action=update rejects section by
// section ("unknown section kind"). An author who read an app back to fix it
// could not write it again — every update failed and the app never changed.

// TestNormalizeSection_RenderedShape feeds a section in the rendered shape
// straight back into the section builder, the exact move that used to fail.
func TestNormalizeSection_RenderedShape(t *testing.T) {
	spec := AppSpec{Slug: "flappy", Name: "Flappy", RecordKey: "id"}
	rendered := []any{
		map[string]any{
			"title": "Flappy",
			"body":  map[string]any{"type": "card", "html": "<!DOCTYPE html><html><body><canvas id=g></canvas></body></html>"},
		},
	}
	page, err := buildAppPage(spec, rendered)
	if err != nil {
		t.Fatalf("rendered-shape section rejected: %v", err)
	}
	if len(page.Sections) != 1 {
		t.Fatalf("sections = %d, want 1", len(page.Sections))
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	// The blob JSON-escapes the markup's angle brackets; match the inner text.
	if !strings.Contains(string(blob), "canvas id=g") {
		t.Fatalf("html did not survive normalization: %s", blob)
	}
}

// TestNormalizeSection_InferredKind covers the other near-miss: the defining
// field is set but `kind` was left off.
func TestNormalizeSection_InferredKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		sec  map[string]any
		want string
	}{
		{"html", map[string]any{"html": "<p>hi</p>"}, "html"},
		{"form", map[string]any{"fields": []any{map[string]any{"field": "a"}}}, "form"},
		{"table", map[string]any{"columns": []any{map[string]any{"field": "a"}}}, "table"},
		{"display", map[string]any{"pairs": []any{map[string]any{"label": "A", "field": "a"}}}, "display"},
		{"chart", map[string]any{"chart_type": "bar", "labels": []any{"a"}, "series": []any{map[string]any{"name": "s", "points": []any{float64(1)}}}}, "chart"},
	} {
		if got := mapStr(normalizeSection(tc.sec), "kind"); got != tc.want {
			t.Errorf("%s: inferred kind %q, want %q", tc.name, got, tc.want)
		}
	}
	// An explicit kind always wins over inference.
	got := normalizeSection(map[string]any{"kind": "empty", "html": "<p>hi</p>"})
	if mapStr(got, "kind") != "empty" {
		t.Errorf("explicit kind overridden: %q", mapStr(got, "kind"))
	}
}

// TestNormalizeSection_NoKindNoHint still errors — inference must not invent a
// kind for a section that names none.
func TestNormalizeSection_NoKindNoHint(t *testing.T) {
	spec := AppSpec{Slug: "x", Name: "X", RecordKey: "id"}
	_, err := buildAppPage(spec, []any{map[string]any{"title": "Mystery"}})
	if err == nil {
		t.Fatal("expected an error for a section with no kind and no hint")
	}
	if !strings.Contains(err.Error(), "action=get") {
		t.Errorf("error should point at action=get for the editable shape: %v", err)
	}
}

// TestAuthoringSectionsFromPage reverses a rendered page back into authoring
// sections — the read path for apps stored before AppSpec.Sections existed.
func TestAuthoringSectionsFromPage(t *testing.T) {
	spec := AppSpec{Slug: "game", Name: "Game", RecordKey: "id"}
	page, err := buildAppPage(spec, []any{
		map[string]any{"kind": "html", "title": "Play", "html": "<!doctype html><html><body>x</body></html>"},
	})
	if err != nil {
		t.Fatalf("buildAppPage: %v", err)
	}
	blob, _ := page.ConfigJSON()
	secs, exact := authoringSectionsFromPage(blob)
	if len(secs) != 1 {
		t.Fatalf("reversed %d sections, want 1", len(secs))
	}
	if !exact {
		t.Error("an html section reverses losslessly; exact should be true")
	}
	if secs[0]["kind"] != "html" {
		t.Errorf("kind = %v, want html", secs[0]["kind"])
	}
	// The reversal must feed straight back into the builder.
	if _, err := buildAppPage(spec, []any{map[string]any(secs[0])}); err != nil {
		t.Fatalf("reversed section rejected by the builder: %v", err)
	}
	// A table reverses to the right kind but is only best-effort.
	page, _ = buildAppPage(spec, []any{
		map[string]any{"kind": "table", "empty_text": "none", "columns": []any{map[string]any{"field": "a"}}},
	})
	blob, _ = page.ConfigJSON()
	secs, exact = authoringSectionsFromPage(blob)
	if len(secs) != 1 || secs[0]["kind"] != "table" {
		t.Fatalf("table reversal = %v", secs)
	}
	if exact {
		t.Error("a table reversal is best-effort; exact should be false")
	}
}

// TestHTMLSectionFramesFullDocument pins the layout-isolation rule: a whole
// document renders in its own frame (its CSS reset can't restyle the app page),
// a fragment stays inlined.
func TestHTMLSectionFramesFullDocument(t *testing.T) {
	spec := AppSpec{Slug: "x", Name: "X", RecordKey: "id"}
	bodyType := func(sections []any) string {
		page, err := buildAppPage(spec, sections)
		if err != nil {
			t.Fatalf("buildAppPage: %v", err)
		}
		blob, _ := page.ConfigJSON()
		var pc struct {
			Sections []struct {
				Body map[string]any `json:"body"`
			} `json:"sections"`
		}
		if err := json.Unmarshal(blob, &pc); err != nil || len(pc.Sections) != 1 {
			t.Fatalf("unmarshal page: %v", err)
		}
		return mapStr(pc.Sections[0].Body, "type")
	}
	full := "<!DOCTYPE html>\n<html><head><style>* { margin: 0 }</style></head><body><canvas></canvas></body></html>"
	if got := bodyType([]any{map[string]any{"kind": "html", "html": full}}); got != "frame" {
		t.Errorf("full document rendered as %q, want frame", got)
	}
	if got := bodyType([]any{map[string]any{"kind": "html", "html": "<p>just a fragment</p>"}}); got != "card" {
		t.Errorf("fragment rendered as %q, want card", got)
	}
}
