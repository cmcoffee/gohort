package ui

// A panel action with Method GET navigates, like a toolbar action.
//
// It used to fetch whatever method it was given, so a GET pulled the target
// page's HTML down, discarded it, and left the screen unchanged. A control
// that fires and changes nothing reads as broken, and that is how the first
// user of it reported it.

import (
	"strings"
	"testing"
)

func TestAPanelActionWithGETNavigates(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	i := strings.Index(src, "ui-display-actions")
	if i < 0 {
		t.Fatal("cannot find the panel action row")
	}
	window := src[i:]
	if len(window) > 2500 {
		window = window[:2500]
	}
	if !strings.Contains(window, "window.location.href = act.url;") {
		t.Error("a GET panel action still fetches and discards the response")
	}
}
