package websearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A published date reaches the agent as its day, its weekday and how long
// ago it was, counted in the user's zone, whatever shape it arrived in.
func TestAPublishedDateSaysHowLongAgo(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no zone data")
	}
	now := time.Date(2026, 10, 29, 11, 0, 0, 0, la) // a Thursday
	cases := []struct{ raw, want string }{
		{"2026-09-28", "Mon, Sep 28, 2026, 31 days ago"},
		{"2026-09-28T10:00:00Z", "Mon, Sep 28, 2026, 31 days ago"},
		{"Sep 27, 2026", "Sun, Sep 27, 2026, 32 days ago"},
		{"September 25, 2026", "Fri, Sep 25, 2026, 34 days ago"},
		{"3 days ago", "Mon, Oct 26, 2026, 3 days ago"},
		{"2026-10-28T23:30:00-07:00", "Wed, Oct 28, 2026, yesterday"},
		// Early morning UTC is still the day before in the user's zone.
		{"2026-10-29T03:00:00Z", "Wed, Oct 28, 2026, yesterday"},
		{"2026-10-29", "Thu, Oct 29, 2026, today"},
		{"2026-03-01", "Sun, Mar 1, 2026, about 8 months ago"},
		{"2023-10-01", "Sun, Oct 1, 2023, about 3 years ago"},
		{"2026-11-02", "Mon, Nov 2, 2026"},
	}
	for _, c := range cases {
		tm, dateOnly, ok := parsePublished(c.raw, now)
		if !ok {
			t.Errorf("%q did not parse", c.raw)
			continue
		}
		if got := publishedPhrase(tm, dateOnly, la, now); got != c.want {
			t.Errorf("%q: %q, want %q", c.raw, got, c.want)
		}
	}
	for _, raw := range []string{"", "recently", "2 months ago"} {
		if _, _, ok := parsePublished(raw, now); ok {
			t.Errorf("%q should not read as a date", raw)
		}
	}
}

// A search backend tags a result's date; web_search renders the tag in the
// user's zone. An unreadable date leaves the snippet as it was.
func TestASearchResultIsDatedForTheReader(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no zone data")
	}
	now := time.Date(2026, 10, 29, 11, 0, 0, 0, la)
	if publishedTag("whenever") != "" || publishedTag("") != "" {
		t.Error("an unreadable date tags nothing")
	}
	results := "1. Advisory lifted\n   https://example.com/a\n   " + publishedTag("2026-09-27") + "Two days after...\n\n" +
		"2. Fix shipped\n   https://example.com/b\n   " + publishedTag("2026-09-28T16:00:00Z") + "On Monday..."
	got := renderPublished(results, la, now)
	for _, want := range []string{"[Published Sun, Sep 27, 2026, 32 days ago] Two days after", "[Published Mon, Sep 28, 2026, 31 days ago] On Monday"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	src := ParseSearchResults(got)
	if len(src) != 2 || src[1].URL != "https://example.com/b" {
		t.Errorf("a dated result still parses as a result: %+v", src)
	}
}

// fetch_url states a page's date above its text, read from the meta tags,
// itemprop or JSON-LD, and says what the page's own day words count from.
func TestAFetchedPageSaysWhenItWasPublished(t *testing.T) {
	now := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	pages := map[string]string{
		"meta":     `<meta property="article:published_time" content="2026-09-28T10:00:00Z">`,
		"itemprop": `<meta itemprop="datePublished" content="2026-09-28">`,
		"json-ld":  `<script type="application/ld+json">{"@type":"NewsArticle","datePublished": "2026-09-28T09:00:00Z"}</script>`,
	}
	for name, html := range pages {
		line := publishedLine(extractMeta(html, SourceMeta{}).PubDate, time.UTC, now)
		if !strings.HasPrefix(line, "Published Mon, Sep 28, 2026, 31 days ago.") || !strings.Contains(line, "not from today") {
			t.Errorf("%s: %q", name, line)
		}
	}
	if publishedLine("", time.UTC, now) != "" {
		t.Error("a page with no date says nothing about one")
	}
}

// A backend that returns a date tags its result with it; one that returns
// none (null, as SearXNG sends) leaves the snippet bare.
func TestASearchBackendTagsTheDateItIsGiven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"Advisory","url":"https://example.com/a","content":"Two days after","publishedDate":"2026-09-27T00:00:00"},{"title":"Other","url":"https://example.com/b","content":"Undated","publishedDate":null}]}`))
	}))
	defer srv.Close()
	got, err := searchSearXNG(SearchRequest{Query: "advisory", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[Published 2026-09-27] Two days after") || !strings.Contains(got, "\n   Undated") {
		t.Errorf("results:\n%s", got)
	}
}
