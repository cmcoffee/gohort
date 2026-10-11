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
