package ui

// A region the APP fills itself. The framework owns the chrome, the app owns
// what is inside it, and core/ui never learns what that is.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAClientRegionNamesAHandlerAndCarriesItsArgs(t *testing.T) {
	b, err := json.Marshal(ClientRegion{
		Action: "some_app_surface",
		Args:   map[string]any{"only": "guardrails", "agent": "a1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"type":"client_region"`, // the renderer dispatches on this
		`"action":"some_app_surface"`,
		`"only":"guardrails"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// The handler runs AFTER the element is in the document. What it mounts
// routinely measures or focuses, and neither works on an element with no
// layout: mountComponent appends what the component returns, so calling
// inline would run against a detached node.
func TestTheRegionMountsItsHandlerAfterTheElementIsInTheDocument(t *testing.T) {
	src := readRuntimeFile(t, "70_misc.js")
	i := strings.Index(src, "components.client_region = function")
	if i < 0 {
		t.Fatal("the renderer is gone")
	}
	body := src[i : i+1200]
	if !strings.Contains(body, "setTimeout(function()") {
		t.Error("the handler runs inline, against an element not yet in the document")
	}
	// A missing handler says so in place rather than leaving a blank area that
	// reads as a surface with nothing in it.
	if !strings.Contains(body, "no handler named") {
		t.Error("an unregistered handler fails silently, leaving an empty region")
	}
	// It goes through the SAME registry as row and view actions, so an app has
	// one way to expose a handler rather than one per component.
	if !strings.Contains(body, "window.UIClientActions") {
		t.Error("the region uses its own registry instead of the shared one")
	}
}

// An app needs to be able to say "that worked" without a dialog. uiAlert takes
// a click to dismiss and reads as a problem.
func TestAppsCanRaiseAToast(t *testing.T) {
	if !strings.Contains(readRuntimeFile(t, "00_prelude.js"), "window.uiToast = function(msg)") {
		t.Error("showToast is a prelude local again, so an app has no way to raise one")
	}
}

// A nav item can name a PAGE, not only a table source. The app declares the
// page once and serves it both ways, so a panel and the standalone page
// cannot drift into two surfaces that merely resemble each other.
func TestANavItemCanRenderADeclaredPage(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	if !strings.Contains(src, "if (item.page_source) {") {
		t.Fatal("a nav item naming a page is ignored")
	}
	i := strings.Index(src, "if (item.page_source) {")
	body := src[i : i+1200]
	// Drawn with the SAME renderer a document uses, not a second one.
	if !strings.Contains(body, "window.uiRenderPageBody(pcfg, orchView)") {
		t.Error("the page is not drawn with the shared page renderer")
	}
	// The panel already sits inside a document: a page header, back arrow and
	// footer here would be two of everything.
	if strings.Contains(body, "uiRenderPage(") {
		t.Error("the panel renders a whole document, chrome included")
	}
	// It must return, or the table path runs too and overwrites it.
	if !strings.Contains(body, "return;") {
		t.Error("the table path still runs after the page is drawn")
	}
}

// A source whose id belongs in the PATH says so with {agent}; one keyed by
// query still gets the ?agent= stamp. A source can need either.
func TestAnAgentCanBeSubstitutedIntoASourcePath(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function orchSourceURL(")
	if i < 0 {
		t.Fatal("orchSourceURL has moved")
	}
	body := src[i : i+900]
	if !strings.Contains(body, "{agent}") {
		t.Error("a path-keyed source cannot name the agent, so it fetches the route literally")
	}
	// Before the query stamp: a path-keyed source that also received ?agent=
	// is harmless, but substituting after would leave the placeholder in the
	// path it was meant to fill.
	if strings.Index(body, "{agent}") > strings.Index(body, "'agent=' +") {
		t.Error("the path substitution runs after the query stamp")
	}
}

// A control in a table without a column header for it says what its options
// are but never what QUESTION it answers. A row carrying two ladders about two
// different situations is a guess either way without captions.
func TestASegmentedRowActionCanCarryACaption(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	i := strings.Index(src, "act.type === 'segmented'")
	if i < 0 {
		t.Fatal("the segmented renderer has moved")
	}
	body := src[i : i+1400]
	if !strings.Contains(body, "ui-row-toggle-label") {
		t.Error("a segmented action cannot carry a caption, so two ladders on one row are indistinguishable")
	}
	// The same pairing a toggle already had, not a second style for the same
	// idea.
	if !strings.Contains(body, "ui-row-toggle-pair") {
		t.Error("the caption uses its own markup instead of the pairing toggles use")
	}
	// Still renders bare when unlabelled: most tables have a header for it.
	if !strings.Contains(body, "if (act.label)") {
		t.Error("the caption is unconditional, so every existing segmented control grows one")
	}
}
