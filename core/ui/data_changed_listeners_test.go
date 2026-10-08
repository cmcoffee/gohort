package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uiInvalidate dispatches ui-data-changed on window, and an event sent to
// window never reaches document. The chart panel listened on document, so a
// chart over a data source kept its old drawing after every form save until
// the page was reloaded, while the display beside it refreshed.
func TestDataChangedListenersAreOnWindow(t *testing.T) {
	files, err := filepath.Glob("assets/runtime/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no runtime files: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "document.addEventListener('ui-data-changed'") {
			t.Errorf("%s listens for ui-data-changed on document, which uiInvalidate's window event never reaches", f)
		}
	}
}
