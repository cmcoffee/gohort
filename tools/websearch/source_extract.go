package websearch

import (
	"regexp"
	"strings"

	"github.com/cmcoffee/oddjob/core/sources"
)

// The pages these tools read, as sources a pipeline run can number and cite
// (core/sources RunSources). Declared here because only these tools know their
// own output format; a run never parses a tool's result by name.

func init() {
	sources.RegisterExtractor("web_search", searchSources)
	sources.RegisterExtractor("fetch_url", fetchSources)
}

// searchResultRE is one result in a search listing, "N. Title" followed by the
// URL and the snippet on the next two lines, indented.
var searchResultRE = regexp.MustCompile(`(?m)^\d+\. (.+)\n\s+(https?://\S+)(?:\n\s+(.*))?`)

// searchSources is each result of a web_search, its snippet as the text: a
// search names a page without reading it, so a later fetch of the same page
// replaces the snippet with the page.
func searchSources(_ map[string]any, result string) []sources.Fetched {
	var out []sources.Fetched
	for _, m := range searchResultRE.FindAllStringSubmatch(result, -1) {
		out = append(out, sources.Fetched{Title: strings.TrimSpace(m[1]), URL: strings.TrimSpace(m[2]), Text: strings.TrimSpace(m[3])})
	}
	return out
}

// fetchHeaderRE is the line fetch_url opens a read page with.
var fetchHeaderRE = regexp.MustCompile(`(?m)^Fetched (\S+) \(\d+ chars\):\n\n`)

// fetchSources is the page a fetch_url read, with its text. The URL comes from
// the result's own header, which holds after a redirect and when the call's
// argument was still a template; the title is the page's first line, which is
// its heading on nearly every article.
func fetchSources(args map[string]any, result string) []sources.Fetched {
	loc := fetchHeaderRE.FindStringSubmatchIndex(result)
	if loc == nil {
		return nil // an error, a saved file, a raw HTTP reply: no page was read
	}
	url := result[loc[2]:loc[3]]
	text := result[loc[1]:]
	// The truncation and cache notes the tool appends are its own words.
	if i := strings.Index(text, "\n\n[Truncated:"); i >= 0 {
		text = text[:i]
	}
	title := url
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(strings.TrimLeft(line, "# ")); len(line) > 3 {
			if len(line) > 120 {
				line = line[:120] + "…"
			}
			title = line
			break
		}
	}
	return []sources.Fetched{{Title: title, URL: url, Text: strings.TrimSpace(text)}}
}
