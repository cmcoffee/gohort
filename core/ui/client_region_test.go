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

// A row carrying a name, a state, an origin, a description and two controls
// does not fit one line, and ellipsizing the description to make it fit cuts
// the part that says what the thing IS.
func TestAColumnCanRenderOnASecondLine(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "function cellHost(col)") {
		t.Fatal("cells all go to one host, so no column can take a second line")
	}
	if !strings.Contains(src, "Number(col.line) !== 2") {
		t.Error("the second line is not opt-in per column")
	}
	// Built only when something asks, so every existing table keeps the
	// single-line layout it was written for.
	i := strings.Index(src, "function cellHost(col)")
	if !strings.Contains(src[i:i+400], "if (!secondLine)") {
		t.Error("the second line is built unconditionally")
	}
	// The two lines STACK, and the row's own flex then lays the stack out
	// beside any actions exactly as it did with one line.
	if !strings.Contains(src, "ui-row-lines") {
		t.Error("the lines are not stacked, so they would sit side by side")
	}
	css := readRuntimeFile(t, "../runtime.css")
	if !strings.Contains(css, ".ui-row-lines") {
		t.Error("the stack has no styling")
	}
	// Top-aligned: a two-line block centred against a single button leaves the
	// name floating off the control it belongs to.
	if !strings.Contains(css, "align-items: flex-start") {
		t.Error("a two-line row is not top-aligned against its actions")
	}
}

// A form where some switches mean "on = allowed" and others mean "on =
// forbidden" is where somebody flips the wrong one. The storage is not free to
// rename - inverting a stored bool inverts every record already written - so
// the toggle turns round what the reader sees and sets, and nothing else.
func TestAToggleCanShowTheOppositeOfANegativeField(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	i := strings.Index(src, "} else if (t === 'toggle') {")
	if i < 0 {
		t.Fatal("the form toggle branch has moved")
	}
	body := src[i : i+1400]
	if !strings.Contains(body, "var inv = !!f.invert;") {
		t.Fatal("a toggle cannot be inverted, so a negative field must be labelled negatively")
	}
	// BOTH directions. Showing the opposite while writing the stored sense
	// would make every click set the value it already had.
	if !strings.Contains(body, "inv ? !initial : !!initial") {
		t.Error("the displayed state is not inverted")
	}
	if !strings.Contains(body, "inv ? !input.checked : input.checked") {
		t.Error("the saved value is not inverted, so the switch writes the opposite of what it shows")
	}
}
