package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestPopupShimWrapped guards the cheap structural invariants of the
// injected shim: it is exactly one <script> element wrapping the embedded
// IIFE. A failure means the embed target or the wrapper drifted.
func TestPopupShimWrapped(t *testing.T) {
	if !strings.HasPrefix(popup_shim_script, "<script>") || !strings.HasSuffix(popup_shim_script, "</script>") {
		t.Fatalf("popup_shim_script must be a single <script> element, got prefix %.10q / suffix %.10q",
			popup_shim_script, popup_shim_script[len(popup_shim_script)-10:])
	}
	js := strings.TrimSpace(popup_shim_js)
	if js == "" {
		t.Fatal("embedded assets/popup_shim.js is empty")
	}
	if !strings.HasPrefix(js, "(function(") {
		t.Errorf("shim should be an IIFE; starts with %.20q", js)
	}
	tail := js
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	if !strings.HasSuffix(js, ")();") {
		t.Errorf("shim IIFE should be self-invoked; ends with %q", tail)
	}
}

// TestPopupShimSyntax runs `node --check` on the embedded shim so a JS
// syntax error fails the test instead of shipping. This is the guard for
// the bug class that bit us twice: the shim is injected as one line, so a
// single missing semicolon throws "Unexpected token" in JavaScriptCore
// and silently kills EVERY popup. Skipped when node isn't installed.
func TestPopupShimSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS syntax check")
	}
	f, err := os.CreateTemp("", "popup_shim_*.js")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(popup_shim_js); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if out, err := exec.Command(node, "--check", f.Name()).CombinedOutput(); err != nil {
		t.Fatalf("assets/popup_shim.js failed `node --check`: %v\n%s", err, out)
	}
}

// TestContextMenuIsNotImageOnly guards the fix for a right click that ate the
// selection instead of offering to copy it.
//
// The menu used to bail unless the target was an <img>, leaving text clicks to
// the webview's native menu. When that menu does not appear — which is the
// platform's business, not ours — the click has already collapsed the
// selection, so the text is gone before a Copy item can be looked for. Taking
// the event unconditionally, and calling preventDefault, is the whole fix; an
// early return on a non-image would silently restore the bug.
func TestContextMenuIsNotImageOnly(t *testing.T) {
	if strings.Contains(popup_shim_js, "if(!img)return;") {
		t.Error("the context menu bails on non-image targets again; text right-clicks will lose the selection")
	}
	for _, want := range []string{
		"function context_at(",       // selection captured AT right-click time
		"window.__desktop_copy_text", // menu Copy rides the one Go-side copy path
	} {
		if !strings.Contains(popup_shim_js, want) {
			t.Errorf("shim is missing %q", want)
		}
	}
}

// TestContextMenuCapturesBeforeItDraws guards the ordering the fix depends on:
// the selection is read, then the default is prevented, and only then is a
// menu built. Reading the selection after preventDefault would still work, but
// building the menu before either is what lets a click land and collapse it.
func TestContextMenuCapturesBeforeItDraws(t *testing.T) {
	capture := strings.Index(popup_shim_js, "var ctx=context_at(e);")
	prevent := strings.Index(popup_shim_js, "e.preventDefault();e.stopPropagation();\n  open_menu(")
	if capture < 0 || prevent < 0 {
		t.Fatalf("contextmenu handler shape changed (capture=%d prevent=%d)", capture, prevent)
	}
	if capture > prevent {
		t.Error("the selection must be captured before the menu is opened")
	}
}
