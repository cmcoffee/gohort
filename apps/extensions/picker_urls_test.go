package extensions

// A picker fetches its options from a URL, and a URL in a page is resolved
// against THAT page, not against the app that wrote it. Two ways to get a 404
// out of that, and this pins both:
//
//   - reaching into a sibling app ("../agents/api/…"), which also makes this
//     app's sharing depend on that app being installed and enabled
//   - guessing the sibling's mount path, which is not the app's own name
//
// Both happened at once here: the skill "Shared with" picker pointed at
// "../agents/api/user-candidates" while the orchestrator app is mounted at
// /orchestrate, so it 404'd for everybody.

import (
	"os"
	"strings"
	"testing"
)

func TestPickerURLsStayInsideThisApp(t *testing.T) {
	raw, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		for _, field := range []string{"OptionsSource:", "RecordSource:", "PostTo:"} {
			if !strings.Contains(line, field) {
				continue
			}
			// A parent-relative hop leaves this app's mount, and an absolute
			// one names a path this app does not own.
			if strings.Contains(line, `"../`) || strings.Contains(line, `"/`) {
				t.Errorf("line %d leaves this app's mount, which resolves against the open page rather than the app:\n  %s",
					i+1, strings.TrimSpace(line))
			}
		}
	}
}
