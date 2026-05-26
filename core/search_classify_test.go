package core

import "testing"

// Regression test: FTC search results page and unrelated consumer alert
// were both tagged tier-1 [gov] purely because of the .gov hostname.
// The URL-shape filter in ClassifySource should downgrade them to "web".
func TestClassifySource_nonArticlePathsDowngraded(t *testing.T) {
	cases := []struct {
		url  string
		want string
		why  string
	}{
		// Broken citations from a prior run:
		{
			url:  "https://search.ftc.gov/consumer-protection?page=7",
			want: "web",
			why:  "search results page on .gov — not a primary document",
		},
		// Consumer alerts on .gov domains are legitimate articles and
		// SHOULD classify as tier-1 gov. The issue with this URL
		// wasn't classification -- it was topical relevance, which
		// is a separate concern from URL shape.
		{
			url:  "https://consumer.ftc.gov/consumer-alerts/2023/01/dont-answer-another-online-quiz-question",
			want: "gov",
			why:  "real article on .gov — topical relevance is a separate concern",
		},
		// General non-article patterns:
		{
			url:  "https://www.nist.gov/search?q=ai+risk",
			want: "web",
			why:  "NIST search endpoint",
		},
		{
			url:  "https://europa.eu/directory/page=3",
			want: "web",
			why:  "directory listing with pagination",
		},
		{
			url:  "https://pmc.ncbi.nlm.nih.gov/search?term=artificial+intelligence",
			want: "web",
			why:  "PMC search page, not an article",
		},
		{
			url:  "https://nature.com/topic/artificial-intelligence/",
			want: "web",
			why:  "topic listing page",
		},
		// NIST SharePoint view switcher and DRAFT placeholder
		// should not classify as authoritative sources.
		{
			url:  "https://randr19.nist.gov/__FriendlyUrls_SwitchView?ReturnUrl=%2F%3Fq%3Daction%2Bsimilar%2Bmechanism%26db%3Dcovidtrend",
			want: "web",
			why:  "SharePoint view switcher with URL-encoded search query in ReturnUrl — not an article",
		},
		{
			url:  "https://pages.nist.gov/NIST-Tech-Pubs/DRAFT.html",
			want: "web",
			why:  "DRAFT placeholder file, not a published article",
		},
		{
			url:  "https://agency.gov/some/page?ReturnUrl=/search?q=something",
			want: "web",
			why:  "redirect wrapper around a search URL",
		},
		{
			url:  "https://example.gov/template.html",
			want: "web",
			why:  "template page",
		},
		{
			url:  "https://example.gov/default.html",
			want: "web",
			why:  "default/placeholder page",
		},
		// Legitimate primary documents on the same domains should still
		// be classified as tier-1:
		{
			url:  "https://www.nist.gov/blogs/blogrige/2024s-ceo-challenge",
			want: "gov",
			why:  "actual NIST blog article",
		},
		{
			url:  "https://pmc.ncbi.nlm.nih.gov/articles/PMC12980058/",
			want: "peer-reviewed",
			why:  "actual PMC article",
		},
		{
			url:  "https://europa.eu/artificial-intelligence-act",
			want: "gov",
			why:  "EU AI Act page, not a directory",
		},
		{
			url:  "https://www.nature.com/articles/d41586-025-04106-0",
			want: "peer-reviewed",
			why:  "actual Nature article",
		},
	}
	for _, c := range cases {
		got := ClassifySource(c.url)
		if got != c.want {
			t.Errorf("ClassifySource(%q) = %q, want %q (%s)", c.url, got, c.want, c.why)
		}
	}
}
