package admin

// A row action could only ever refresh its own table. Three of the ones
// on this page do their real work somewhere ELSE on it — a catalog
// install lands drafts in four sections, approving a promotion shares a
// tool listed two sections down, and deleting a credential breaks every
// tool that names it — and each left the section it affected showing
// the answer from before the click.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// actionBlock returns the source of the row action that posts to want,
// up to the end of its literal.
func actionBlock(t *testing.T, src, want string) string {
	t.Helper()
	// PostTo, not any mention: the same URL is also a form's Source and a
	// picker's RecordSource, and those are reads.
	loc := regexp.MustCompile(`PostTo:\s*"`+regexp.QuoteMeta(want)+`"`).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("no action posts to %q any more", want)
	}
	i := loc[0]
	// Back up to the start of the literal, forward to its close.
	start := strings.LastIndex(src[:i], "{Type:")
	if start < 0 {
		start = i
	}
	end := strings.Index(src[i:], "},\n")
	if end < 0 {
		t.Fatalf("cannot find the end of the action posting to %q", want)
	}
	return src[start : i+end]
}

func TestTheActionsThatChangeAnotherSectionSaySo(t *testing.T) {
	b, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	for _, tc := range []struct {
		post string
		want []string
		why  string
	}{
		{
			post: "api/catalog?action=install&id={id}",
			want: []string{"api/connectors", "api/persistent-tools", "api/secure-api", "api/skills"},
			why:  "an install lands drafts in every one of those sections, and reviewing them is the next thing you do",
		},
		{
			post: "api/promotions?action=approve&id={id}",
			want: []string{"api/persistent-tools"},
			why:  "approving shares the tool, which is a badge on its row in the table this queue feeds",
		},
		{
			post: "api/secure-api?name={name}",
			want: []string{"api/persistent-tools"},
			why:  "every tool naming a deleted credential is broken, and the page draws that as ⚠ missing",
		},
	} {
		block := actionBlock(t, src, tc.post)
		if !strings.Contains(block, "Invalidate") {
			t.Errorf("%s refreshes nothing else: %s", tc.post, tc.why)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(block, `"`+w+`"`) {
				t.Errorf("%s should invalidate %s — %s", tc.post, w, tc.why)
			}
		}
	}

	// The file import has done this since it existed; the catalog install
	// is the same importer reached another way, so the two lists match.
	if !strings.Contains(src, `uiInvalidate(['api/connectors','api/persistent-tools','api/secure-api','api/skills'])`) {
		t.Error("the file-import path should still invalidate the same four sections")
	}
}
