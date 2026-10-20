package browser

// Every page opens in a throwaway context. One Chromium profile is shared by
// every user of the server and by every peer that borrows its browser, so a
// page opened in the profile itself would carry the cookies and storage
// somebody else left there. Read from the source, since a test run has no
// Chromium to drive.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEveryPageOpensInAThrowawayContext(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	// A page made straight off the shared browser handle, not an incognito one.
	direct := regexp.MustCompile(`\b(b|t\.browser|shared\.browser)\.(MustPage|Page)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if direct.MatchString(line) {
				t.Errorf("%s:%d opens a page in the shared profile: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
