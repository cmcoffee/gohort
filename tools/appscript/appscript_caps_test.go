package appscript

import (
	"strings"
	"testing"
)

func TestMergeCaps(t *testing.T) {
	got := mergeCaps([]string{"fetch", "log", ""}, []string{"fetch_via:ts3_api", "fetch", "  "})
	want := []string{"fetch", "log", "fetch_via:ts3_api"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mergeCaps = %v, want %v (dedup + drop-empty, preserve order)", got, want)
	}
	// An explicit fetch_via declaration must survive and never double when the
	// owner auto-grant would add the same one.
	got = mergeCaps([]string{"fetch_via:ts3_api"}, []string{"fetch_via:ts3_api"})
	if len(got) != 1 || got[0] != "fetch_via:ts3_api" {
		t.Errorf("mergeCaps dedup across lists = %v, want [fetch_via:ts3_api]", got)
	}
}

func TestOwnerFetchViaCapsEmptyOwner(t *testing.T) {
	if got := ownerFetchViaCaps(""); got != nil {
		t.Errorf("ownerFetchViaCaps(\"\") = %v, want nil", got)
	}
	if got := ownerFetchViaCaps("   "); got != nil {
		t.Errorf("ownerFetchViaCaps(blank) = %v, want nil", got)
	}
}
