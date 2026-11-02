package extensions

// The rail holds four places: Integrations, Connected accounts, Tools and
// Skills. What other people or the deployment offer sits INSIDE the matching
// place, not as a rail entry of its own: a key lent to you is a row among
// your integrations, and "Global tools" and the deployment's skills are the
// second part (ui.Subsection) of Tools and Skills. Each reads as a separate
// destination when it is the other half of one.

import (
	"os"
	"regexp"
	"testing"
)

func TestExtensionsRailHoldsFourPlaces(t *testing.T) {
	var src string
	for _, f := range []string{"integrations.go", "extensions.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src += string(b)
	}
	top := regexp.MustCompile(`(?m)^\treturn ui\.Section\{\n\t\tTitle: +"([^"]+)",`).FindAllStringSubmatch(src, -1)
	var got []string
	for _, m := range top {
		got = append(got, m[1])
	}
	want := []string{"Integrations", "Connected accounts", "Tools", "Skills"}
	if len(got) != len(want) {
		t.Fatalf("rail sections = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rail sections = %q, want %q", got, want)
		}
	}
	for _, part := range []string{"Your tools", "Categories", "Global tools", "Your skills", "Published by your deployment"} {
		if !regexp.MustCompile(`ui\.Subsection\{\s*Title:\s*"` + regexp.QuoteMeta(part) + `"`).MatchString(src) {
			t.Errorf("%q should be a part of its section", part)
		}
	}
}
