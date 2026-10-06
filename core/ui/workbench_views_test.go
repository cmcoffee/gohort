package ui

// On a phone a workbench shows its viewer and its chat one at a time, behind a
// switch in the drawer header. Stacked, each got a sliver of a small screen and
// neither was readable (the document sat in about a third of the screen
// under the chat).

import (
	"regexp"
	"strings"
	"testing"
)

func TestWorkbenchPhoneShowsOnePaneAtATime(t *testing.T) {
	phone := regexp.MustCompile(`(?s)@media \(max-width: 700px\) \{\s*\.ui-wb \{.*?\n\}`).FindString(runtimeCSS)
	if phone == "" {
		t.Fatal("the workbench's phone block is gone from runtime.css")
	}
	for _, want := range []string{
		`.ui-wb[data-view="viewer"] > .ui-wb-chat { display: none; }`,
		`.ui-wb[data-view="chat"] > .ui-wb-viewer { display: none; }`,
		`.ui-wb-views {`,
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("phone layout lost %q", want)
		}
	}
	if strings.Contains(phone, "flex: 0 0 42%") {
		t.Error("the chat is back to a stacked 42% slice under the viewer")
	}
	if !strings.Contains(runtimeCSS, ".ui-wb-close, .ui-wb-views { display: none; }") {
		t.Error("the switch must stay hidden on a wide screen, where both panes show")
	}

	js := readRuntimeFile(t, "70_misc.js")
	for _, want := range []string{
		"class: 'ui-wb-views', role: 'tablist'",
		"cfg.viewer_label || 'Document'",
		"cfg.chat_label || 'Assistant'",
		"root.setAttribute('data-view', 'viewer');",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("workbench renderer lost %q", want)
		}
	}
	// Picking a record from the drawer is a request to read it.
	if !regexp.MustCompile(`drawer\.closeDrawer\(\);\s*showPane\('viewer'\);`).MatchString(js) {
		t.Error("picking an item should land on the viewer")
	}
}

// On a phone the viewer toolbar keeps one row and moves what does not fit into
// a More menu. The moved buttons must still follow the selection, which is
// only true if the enable pass looks in the menu as well as the bar.
func TestWorkbenchToolbarOverflowsOnAPhone(t *testing.T) {
	js := readRuntimeFile(t, "70_misc.js")
	for _, want := range []string{
		"class: 'ui-wb-action-wrap ui-wb-more'",
		"window.matchMedia('(max-width: 700px)')",
		"var scopes = [actionBar, headActions, moreMenu];",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("workbench toolbar overflow lost %q", want)
		}
	}
	// The More button is never disabled by the selection, or an empty
	// workbench on a phone could not reach its library actions.
	if !regexp.MustCompile(`moreBtn = el\('button', \{[^}]*'data-ui-lib-action': '1'`).MatchString(js) {
		t.Error("the More button must be exempt from selection gating")
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 700px\) \{\s*\.ui-wb \{.*?\n\}`).FindString(runtimeCSS)
	if !strings.Contains(phone, ".ui-wb-actions { flex-wrap: nowrap; overflow: hidden; }") {
		t.Error("on a phone the toolbar must hold one row for the overflow to measure against")
	}
}

// A Subsection carries its title, subtitle, detail and body to the renderer.
func TestSubsectionMarshalsItsParts(t *testing.T) {
	b, err := Subsection{Title: "Shared with you", Subtitle: "Lent to you.", Detail: "More.",
		Body: Stack{}}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"type":"subsection"`, `"title":"Shared with you"`, `"subtitle":"Lent to you."`, `"detail":"More."`, `"body":{"type":"stack"`} {
		if !strings.Contains(js, want) {
			t.Errorf("subsection JSON lost %s: %s", want, js)
		}
	}
	if !strings.Contains(readRuntimeFile(t, "10_basics.js"), "components.subsection = function(cfg, ctx)") {
		t.Error("no renderer for subsection")
	}
}
