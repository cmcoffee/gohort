package knowledge

import (
	"os"
	"strings"
	"testing"
)

// The old /documents path answers through the shared legacy mount (a 308
// that keeps a POST a POST, past the app-grant check), not a hidden app of
// its own that answered 302 and refused anyone not granted that app.
func TestTheOldDocumentsPathIsALegacyMount(t *testing.T) {
	src, err := os.ReadFile("knowledge.go")
	if err != nil {
		t.Fatal(err)
	}
	if knowledgeLegacyPath != "/documents" || !strings.Contains(string(src), "RegisterLegacyMount(knowledgeLegacyPath, T.WebPath())") {
		t.Error("Knowledge no longer keeps /documents answering")
	}
	if _, err := os.Stat("documents_redirect.go"); err == nil {
		t.Error("the hidden redirect app is back beside the legacy mount")
	}
}

// Choosing an agent to keep a collection says what comes next: what it can do
// with it, and that it waits to be asked or scheduled.
func TestTheCuratorSettingSaysWhatComesNext(t *testing.T) {
	src, err := os.ReadFile("knowledge.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{"Then tell it what to fetch in a chat, or give it a schedule.", "a file a tool saved to its workspace", "It does nothing on its own"} {
		if !strings.Contains(s, want) {
			t.Errorf("the curator setting no longer says %q", want)
		}
	}
}
