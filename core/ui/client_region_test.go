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
