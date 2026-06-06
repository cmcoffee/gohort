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
