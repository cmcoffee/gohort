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
