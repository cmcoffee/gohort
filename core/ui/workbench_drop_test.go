package ui

import (
	"strings"
	"testing"
)

// A picture dropped on the rendered document, with no editor open, is sent
// to the upload URL with place=1 and the section it landed on, and the
// record reloads; a paste goes to the end. Source-scan, like the other
// runtime tests.
func TestAPictureDroppedOnTheDocumentIsPlaced(t *testing.T) {
	src := mustRuntimePart(t, "70_misc.js")
	for _, want := range []string{
		"function placePicture(file, sectionId)",
		"'place=1'",
		"closest('[data-section-id]')",
		"viewerBody.addEventListener('drop'",
		"document.addEventListener('paste'",
		"loadViewer(selectedId);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the viewer no longer places a dropped picture: missing %s", want)
		}
	}
}
