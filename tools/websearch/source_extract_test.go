package websearch

import "testing"

// The two tools' own result formats, read back as the pages they name.
func TestSearchAndFetchResultsBecomeSources(t *testing.T) {
	search := "1. First Result\n   https://one.example/a\n   A snippet about it.\n\n2. Second Result\n   https://two.example/b\n   Another snippet."
	got := searchSources(nil, search)
	if len(got) != 2 || got[0].Title != "First Result" || got[0].URL != "https://one.example/a" || got[0].Text != "A snippet about it." || got[1].URL != "https://two.example/b" {
		t.Errorf("search results read as %+v", got)
	}
	fetch := "[Heads up: thin page.]\n\nFetched https://one.example/a (120 chars):\n\n# The Article Title\nBody text of the article.\n\n[Truncated: full 9000 chars cached at x. Use read_file to access the rest.]"
	pages := fetchSources(map[string]any{"url": "{item}"}, fetch)
	if len(pages) != 1 || pages[0].URL != "https://one.example/a" || pages[0].Title != "The Article Title" {
		t.Fatalf("fetch read as %+v", pages)
	}
	if pages[0].Text != "# The Article Title\nBody text of the article." {
		t.Errorf("the tool's own notes must not be taken as page text: %q", pages[0].Text)
	}
	if fetchSources(nil, "HTTP 404 Not Found\n\nnope") != nil {
		t.Error("a failed fetch read no page")
	}
}
