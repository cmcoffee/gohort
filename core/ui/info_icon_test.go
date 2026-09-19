package ui

// The ⓘ affordance. A config surface has two readers at once: one scanning for
// the knob they came for, one who has stopped on it and now wants to know what
// it does. Writing the whole paragraph inline serves the second and buries the
// first, so the visible line stays one sentence and the rest moves behind the
// icon. Nothing is deleted by moving it; these pin that it is still REACHABLE.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheInfoIconIsAPrimitiveAppsCanReach(t *testing.T) {
	js := runtimeJS
	for _, want := range []string{
		"window.uiInfoIcon = uiInfoIcon;",
		"window.uiAttachInfo = uiAttachInfo;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the runtime does not export %q, so an app cannot reach it", want)
		}
	}
	at := strings.Index(js, "function uiInfoIcon(")
	end := strings.Index(js, "window.uiInfoIcon = uiInfoIcon;")
	if at < 0 || end < at {
		t.Fatal("uiInfoIcon moved")
	}
	block := js[at:end]
	// A title attribute here gives every icon TWO tooltips: the panel at
	// 120ms and the browser's own about a second later, drawn over it. It
	// was added as a no-JS fallback for a case that cannot happen, since
	// the button is built by this same function. The popover describes the
	// button instead, which is the part a screen reader needs.
	if strings.Contains(block, "title: detail") {
		t.Error("the icon sets a native title, so the browser tooltip doubles up with the popover")
	}
	if !strings.Contains(block, "aria-describedby") {
		t.Error("the popover is not tied to the button, so a screen reader gets the label and nothing else")
	}
	// Hover alone is not an affordance on a touch device, and not reachable
	// from a keyboard at all.
	for _, ev := range []string{"'mouseenter'", "'focus'", "'click'"} {
		if !strings.Contains(block, ev) {
			t.Errorf("the icon does not open on %s", ev)
		}
	}
	// Escape must close the popover WITHOUT closing the dialog the icon sits
	// in, which means getting the event before the modal stack's own handler.
	if !strings.Contains(block, "window.addEventListener('keydown', onInfoKey, true)") ||
		!strings.Contains(block, "ev.stopPropagation()") {
		t.Error("Escape in an open popover would also dismiss the surrounding modal")
	}
}

// Every surface that renders a Help line renders the Detail beside it, or a
// field's long copy is written and unreachable, which is worse than not
// writing it.
func TestEveryHelpSurfaceCarriesItsDetail(t *testing.T) {
	js := runtimeJS
	for what, want := range map[string]string{
		"a form field's label":  "el('label', {class: 'ui-form-label'}, [f.label]), f.detail)",
		"a section header":      "el('div', {class: 'ui-form-section-title'}, [f.label]), f.detail)",
		"a toggle row":          "window.uiInfoIcon(t.detail)",
		"a checklist option":    "if (o.detail) {",
		"a sections slot":       "[s.spec.help || '']), s.spec.detail)",
		"a field with no label": "if (f.detail && !f.label) {",
		"a page section":        "window.uiInfoIcon(s.detail)",
		"a no-chrome section":   "s.title ? s.detail : ''",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("%s drops its detail: %q missing", what, want)
		}
	}
}

// The field serializes, or the server can write one and the browser never
// sees it.
func TestDetailRidesTheWire(t *testing.T) {
	for _, c := range []struct {
		what string
		v    any
	}{
		{"FormField", FormField{Field: "f", Detail: "why"}},
		{"SelectOption", SelectOption{Value: "v", Detail: "why"}},
		{"SectionSpec", SectionSpec{Title: "t", Detail: "why"}},
		{"Toggle", Toggle{Field: "f", Detail: "why"}},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if !strings.Contains(string(b), `"detail":"why"`) {
			t.Errorf("%s does not serialize Detail: %s", c.what, b)
		}
	}
	// Absent by default: a form of fields with nothing extra to say should
	// not grow an icon per row.
	b, _ := json.Marshal(FormField{Field: "f"})
	if strings.Contains(string(b), "detail") {
		t.Errorf("an empty Detail should be omitted, got %s", b)
	}
}

// Section is not marshalled directly — it is copied into the page config the
// browser parses — so the field is only real if that copy carries it.
func TestASectionsDetailReachesThePage(t *testing.T) {
	render := func(p Page) string {
		w := httptest.NewRecorder()
		if err := p.Render(w); err != nil {
			t.Fatalf("render: %v", err)
		}
		return w.Body.String()
	}
	html := render(Page{
		Title: "T",
		Sections: []Section{{
			Title:    "External credentials",
			Subtitle: "The APIs you've been granted.",
			Detail:   "The long half, behind the icon.",
		}},
	})
	if !strings.Contains(html, "The long half, behind the icon.") {
		t.Fatal("a Section's Detail never reaches the page config, so the icon has nothing to show")
	}
	if !strings.Contains(html, `"detail"`) {
		t.Error("Detail is not serialized under the key the runtime reads")
	}
	// Absent by default: a page of ordinary sections should not gain a key per
	// section for a field none of them set.
	plain := render(Page{Title: "T", Sections: []Section{{Title: "Plain"}}})
	if strings.Contains(plain, `"detail"`) {
		t.Error("an empty Detail should be omitted from the page config")
	}
}
