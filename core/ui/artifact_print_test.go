package ui

import (
	"strings"
	"testing"
)

// The artifact pane can print what it shows, which is also how an artifact
// becomes a PDF. An authored document is sandboxed, so the pane asks it to
// print itself by message, and the frame needs allow-modals to be allowed to.
// Source-scan, like the other runtime tests.
func TestTheArtifactPaneCanPrint(t *testing.T) {
	src := mustRuntimePart(t, "00_prelude.js")
	for _, want := range []string{
		"printBtn.title = 'Print, or save as PDF';",
		"hdr.appendChild(printBtn);",
		"e.source===window.parent&&e.data&&e.data.__uiArtifactPrint)window.print();",
		"var shim = STORAGE_SHIM + PRINT_SHIM",
		"frame.setAttribute('sandbox', 'allow-scripts allow-modals');",
		"frame.contentWindow.postMessage({__uiArtifactPrint: 1}, '*');",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the artifact pane cannot print: missing %s", want)
		}
	}
	if strings.Contains(src, "frame.setAttribute('sandbox', 'allow-scripts allow-same-origin") {
		t.Error("an authored artifact must never get allow-same-origin")
	}
}

// The pane downloads an authored artifact as a file, stubbing the live-data
// helper a file on disk cannot reach, and offers one Save button per place
// the server lists, naming none of them itself.
func TestTheArtifactPaneDownloadsAndSaves(t *testing.T) {
	src := mustRuntimePart(t, "00_prelude.js")
	for _, want := range []string{
		"downloadBtn.title = 'Download as an HTML file';",
		"a.download = artifactFileName();",
		"This saved copy has no live data.",
		"fetch('/api/document-sinks', {credentials: 'same-origin'})",
		"fetch('/api/document-sinks/save?name=' + encodeURIComponent(sink.name)",
		"downloadBtn.style.display = authored ? '' : 'none';",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the artifact pane lost download or save: missing %s", want)
		}
	}
	// Each button's text is the server's label, not one the pane wrote.
	if !strings.Contains(src, "b.textContent = String(sink.label || sink.name);") {
		t.Error("a Save button's text no longer comes from the server's list of places")
	}
}
