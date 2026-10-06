package extensions

// The rail holds four places: APIs, Connected accounts, Tools and Skills. What
// other people or the deployment offer sits INSIDE the matching place as its
// second part (ui.Subsection), not as a rail entry of its own: "Shared with
// you", "Global tools" and the deployment's skills each read as a separate
// destination when they are the other half of one.

import (
	"os"
	"regexp"
	"testing"
)

func TestExtensionsRailHoldsFourPlaces(t *testing.T) {
	b, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	top := regexp.MustCompile(`(?m)^\t\t\tTitle: +"([^"]+)",`).FindAllStringSubmatch(src, -1)
	var got []string
	for _, m := range top {
		got = append(got, m[1])
	}
	want := []string{"APIs", "Connected accounts", "Tools", "Skills"}
	if len(got) != len(want) {
		t.Fatalf("rail sections = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rail sections = %q, want %q", got, want)
		}
	}
	for _, part := range []string{"Your APIs", "Shared with you", "Your tools", "Global tools", "Your skills", "Published by your deployment"} {
		if !regexp.MustCompile(`ui\.Subsection\{\s*Title:\s*"` + regexp.QuoteMeta(part) + `"`).MatchString(src) {
			t.Errorf("%q should be a part of its section", part)
		}
	}
}
