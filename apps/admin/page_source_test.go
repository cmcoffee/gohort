package admin

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// adminPageSource is the admin page as one text: page.go plus every
// page_<area>.go builder and the web_assets.go blobs. The source-reading tests
// below used to open page.go alone, which held the whole page until it was cut
// into one file per tab area; a check that counts forms or looks for a help
// string has to see all of them or it reads a split as a regression.
func adminPageSource(t *testing.T) string {
	t.Helper()
	names, err := filepath.Glob("page*.go")
	if err != nil {
		t.Fatal(err)
	}
	names = append(names, "web_assets.go")
	sort.Strings(names)
	var all []byte
	for _, n := range names {
		if filepath.Ext(n) != ".go" || len(n) > 8 && n[len(n)-8:] == "_test.go" {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
		all = append(all, '\n')
	}
	return string(all)
}
