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

// Choosing how it is curated says what comes next: who runs the plan and
// when, that Run now runs it once either way, and what an agent curating it
// can do with the collection.
func TestTheCuratorSettingSaysWhatComesNext(t *testing.T) {
	src, err := os.ReadFile("knowledge.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{"Run now runs it once whichever you choose.", "Hand Curated runs it only when you press Run now", "a file its tools saved", "each page checked before it goes in"} {
		if !strings.Contains(s, want) {
			t.Errorf("the curator setting no longer says %q", want)
		}
	}
}
