package extensions

// An API's edit form has no key field: the key is set under Connected
// accounts. The heading that stands in for it says so and links there, since
// a heading alone read as a form with nowhere to paste the key.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTheKeyHeadingLinksToConnectedAccounts(t *testing.T) {
	var key *struct{ help, url, label string }
	for _, f := range credentialEditFields() {
		if f.Type == "header" && f.Label == "Key" {
			if len(f.Links) != 1 {
				t.Fatalf("the Key heading has %d links, want the one to Connected accounts", len(f.Links))
			}
			key = &struct{ help, url, label string }{f.Help, f.Links[0].URL, f.Links[0].Label}
		}
		if f.Field == "secret" {
			t.Error("the edit form offers a key field again: the key is set under Connected accounts")
		}
	}
	if key == nil {
		t.Fatal("the edit form has no Key heading")
	}
	if !strings.Contains(key.help, "Connected accounts") || !strings.Contains(key.label, "Connected accounts") {
		t.Errorf("the heading should say where the key goes: help %q, link %q", key.help, key.label)
	}

	// The link lands on the section by its title, so it must name the slug of
	// a section that is really called that.
	path, frag, ok := strings.Cut(key.url, "#")
	if !ok || path != "/extensions" {
		t.Fatalf("link %q should open this page at a section", key.url)
	}
	b, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range regexp.MustCompile(`(?m)^\t\t\tTitle: +"([^"]+)",`).FindAllStringSubmatch(string(b), -1) {
		slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(m[1]), "-"), "-")
		if slug == frag {
			found = true
		}
	}
	if !found {
		t.Errorf("#%s names no section on the page: renaming Connected accounts means changing connectedAccountsURL", frag)
	}
}
