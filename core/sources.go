package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SourceRef is a single numbered source entry.
type SourceRef struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Authors string `json:"authors,omitempty"` // "LastName, F.; OtherLast, G." — populated by EnrichMetadata for peer-reviewed/preprint URLs
	Journal string `json:"journal,omitempty"` // exact journal name from citation_journal_title meta tag — avoids domain-to-journal confusion (e.g., nature.com may host Nature, Scientific Reports, Nature Communications, etc.)
}

// NumberedSource is a source entry with its original numbering,
// used for absorption/remapping between registries.
type NumberedSource struct {
	N     int
	Title string
	URL   string
}

// SourceRegistry is a thread-safe, URL-deduplicated, 1-based numbered
// source index. Used by synthesis and cross-session merging paths.
type SourceRegistry struct {
	mu    sync.Mutex
	refs  []SourceRef
	byURL map[string]int // NormalizeURL(url) → 1-based index
}

// Register adds a source and returns its 1-based index. If the URL is
// already registered, returns the existing index. Thread-safe.
func (sr *SourceRegistry) Register(title, rawURL string) int {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), ".,;:)")
	if rawURL == "" {
		return 0
	}
	key := NormalizeURL(rawURL)
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.byURL == nil {
		sr.byURL = make(map[string]int)
	}
	if idx, ok := sr.byURL[key]; ok {
		return idx
	}
	t := strings.TrimSpace(title)
	if t == "" {
		t = rawURL
	}
	sr.refs = append(sr.refs, SourceRef{Title: t, URL: rawURL})
	idx := len(sr.refs)
	sr.byURL[key] = idx
	return idx
}

// Lookup returns the 1-based index for a URL, or 0 if not registered.
func (sr *SourceRegistry) Lookup(rawURL string) int {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), ".,;:)")
	if rawURL == "" {
		return 0
	}
	key := NormalizeURL(rawURL)
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.byURL == nil {
		return 0
	}
	return sr.byURL[key]
}

// Refs returns a thread-safe snapshot of all registered source refs.
func (sr *SourceRegistry) Refs() []SourceRef {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	out := make([]SourceRef, len(sr.refs))
	copy(out, sr.refs)
	return out
}

// Len returns the number of registered sources.
func (sr *SourceRegistry) Len() int {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return len(sr.refs)
}

// Absorb merges sources from another index into this registry and
// returns a remap from old numbering to new numbering. Used for
// cross-session source bridging (e.g., child session → parent
// session, or multiple source reports → combined report).
func (sr *SourceRegistry) Absorb(entries []NumberedSource) map[int]int {
	remap := make(map[int]int)
	for _, e := range entries {
		newN := sr.Register(e.Title, e.URL)
		if e.N > 0 && newN > 0 {
			remap[e.N] = newN
		}
	}
	return remap
}

// FormatCitationList builds the debater-facing source list:
//
//	[1] Title
//	    URL
//	[2] Title
//	    URL
func (sr *SourceRegistry) FormatCitationList() string {
	refs := sr.Refs()
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n--- AVAILABLE SOURCES ---\n")
	b.WriteString("Cite sources by number only, e.g. [1], [3]. Do NOT reproduce URLs in your argument. Citing a number not in this list will be flagged as fabricated.\n\n")
	for i, e := range refs {
		fmt.Fprintf(&b, "[%d] %s\n    %s\n", i+1, e.Title, e.URL)
	}
	return b.String()
}

// FormatBibliography builds the tagged source index for synthesis
// prompts and final reports:
//
//	[1] [peer-reviewed] Title - Authors: LastName et al. - URL
//	[2] [peer-reviewed] Title - AUTHORS UNAVAILABLE - URL
//	[3] [edu] Title - URL
//
// Authors are included when SourceRef.Authors is populated (by
// EnrichMetadata). For peer-reviewed/preprint sources with no authors
// populated, renders an explicit "AUTHORS UNAVAILABLE" marker so
// downstream prose generators have an unambiguous signal rather
// than silence — prevents the writer from inventing names.
func (sr *SourceRegistry) FormatBibliography() string {
	refs := sr.Refs()
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range refs {
		title := s.Title
		// Avoid "URL - URL" duplication when no real title is available.
		if title == s.URL || title == "" {
			title = CleanSourceTitle("", s.URL)
		}
		tag := ClassifySource(s.URL)
		authorFragment := ""
		if s.Authors != "" {
			authorFragment = " - Authors: " + s.Authors
		} else if tag == "peer-reviewed" || tag == "preprint" {
			authorFragment = " - AUTHORS UNAVAILABLE"
		}
		journalFragment := ""
		if s.Journal != "" {
			journalFragment = " - Journal: " + s.Journal
		}
		if tag != "" && tag != "web" {
			fmt.Fprintf(&b, "[%d] [%s] %s%s%s - %s\n", i+1, tag, title, authorFragment, journalFragment, s.URL)
		} else {
			fmt.Fprintf(&b, "[%d] %s%s%s - %s\n", i+1, title, authorFragment, journalFragment, s.URL)
		}
	}
	return b.String()
}

// ExpandSourcesSection finds the ## SOURCES section in text and
// rebuilds it from the registry. Extracts which [N] numbers the model
// cited (including comma-separated groups like [1, 3, 7]), then
// replaces the entire section with clean markdown-linked entries.
func (sr *SourceRegistry) ExpandSourcesSection(text string) string {
	idx := strings.Index(text, "## SOURCES")
	if idx < 0 {
		return text
	}
	before := text[:idx]
	sources_section := text[idx:]
	refs := sr.Refs()

	// Collect cited numbers from bracketed groups [N] and [N, N, N].
	bracketRE := regexp.MustCompile(`\[[\d,\s]+\]`)
	numRE := regexp.MustCompile(`\d+`)
	seen := make(map[int]bool)
	var cited []int
	for _, bracket := range bracketRE.FindAllString(sources_section, -1) {
		for _, match := range numRE.FindAllString(bracket, -1) {
			n := 0
			for _, c := range match {
				n = n*10 + int(c-'0')
			}
			if n >= 1 && n <= len(refs) && !seen[n] {
				seen[n] = true
				cited = append(cited, n)
			}
		}
	}
	if len(cited) == 0 {
		return text
	}

	var b strings.Builder
	b.WriteString("## SOURCES\n")
	for _, n := range cited {
		ref := refs[n-1]
		fmt.Fprintf(&b, "\n[%d] [%s](%s)\n", n, ref.Title, ref.URL)
	}
	return before + b.String()
}

// ExtractCitedURLs extracts [N] citations from text and resolves
// them to URLs via the registry.
func (sr *SourceRegistry) ExtractCitedURLs(text string) []string {
	var urls []string
	seen := make(map[string]bool)
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for _, match := range CiteRefPattern.FindAllStringSubmatch(text, -1) {
		n := 0
		for _, c := range match[1] {
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= len(sr.refs) {
			u := sr.refs[n-1].URL
			if u != "" && !seen[u] {
				urls = append(urls, u)
				seen[u] = true
			}
		}
	}
	return urls
}

// RewriteCitations replaces [N] references in text using a remap.
// Numbers not in the remap are dropped (replaced with empty string)
// to prevent misattribution when indices don't correspond.
func RewriteCitations(text string, remap map[int]int) string {
	return CiteRefPattern.ReplaceAllStringFunc(text, func(match string) string {
		var n int
		fmt.Sscanf(match, "[%d]", &n)
		if newN, ok := remap[n]; ok {
			return fmt.Sprintf("[%d]", newN)
		}
		return ""
	})
}

// NormalizeURL reduces a URL to a comparable key: lowercased host
// (www. stripped) + path with trailing slashes stripped. Query strings
// and fragments are dropped.
func NormalizeURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:)")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimRight(raw, "/"))
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	path := strings.TrimRight(u.Path, "/")
	return host + path
}

// citationAuthorRE matches the Google Scholar / Highwire-standard
// citation_author meta tag used by most peer-reviewed publishers
// (PMC, Oxford Academic, Sage, Frontiers, MDPI, Wiley, Nature, arXiv,
// etc.). One tag per author — multiple matches expected per page.
// Handles single and double quotes; content attribute may come before
// or after the name attribute.
var citationAuthorRE = regexp.MustCompile(
	`<meta\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_author["']\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']` +
		`|<meta\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_author["']`,
)

// citationTitleRE matches citation_title — same standard, one per page.
var citationTitleRE = regexp.MustCompile(
	`<meta\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_title["']\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']` +
		`|<meta\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_title["']`,
)

// citationJournalRE matches citation_journal_title — disambiguates the
// actual journal from the hosting domain. Matters for publishers that
// host multiple journals on the same site (nature.com → Nature vs.
// Scientific Reports vs. Nature Communications; sciencedirect.com →
// many Elsevier journals). Without this, the writer infers the journal
// from the domain and can call a Scientific Reports paper "Nature."
var citationJournalRE = regexp.MustCompile(
	`<meta\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_journal_title["']\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']` +
		`|<meta\s+(?:[^>]*?\s+)?content\s*=\s*["']([^"']+)["']\s+(?:[^>]*?\s+)?name\s*=\s*["']citation_journal_title["']`,
)

// fetchCitationMetadata fetches a URL and extracts citation_author,
// citation_title, and citation_journal_title meta tags. Returns
// authors joined as "A; B; C", the citation_title, and the
// citation_journal_title (each empty when missing). Bounded body read
// (256KB — meta tags appear in <head>, no need to buffer entire HTML)
// and a short per-request timeout so a single slow host can't stall
// enrichment.
//
// Silent on every failure path (bad URL, DNS, TLS, non-200, parse) —
// caller falls back to the pre-existing title and AUTHORS UNAVAILABLE
// marker, which is correct behavior for a best-effort enrichment.
func fetchCitationMetadata(ctx context.Context, rawURL string) (authors, title, journal string) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", "", ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; gohort-research/1.0; +https://github.com/cmcoffee/gohort)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	client := &http.Client{
		Timeout: 8 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", "", ""
	}
	html := string(body)

	var names []string
	seen := make(map[string]bool)
	for _, m := range citationAuthorRE.FindAllStringSubmatch(html, -1) {
		// Either group 1 or group 2 captured depending on attribute order.
		name := m[1]
		if name == "" {
			name = m[2]
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
		if len(names) >= 12 {
			break // cap — bibliography line stays readable
		}
	}
	authors = strings.Join(names, "; ")

	if m := citationTitleRE.FindStringSubmatch(html); m != nil {
		title = m[1]
		if title == "" {
			title = m[2]
		}
		title = strings.TrimSpace(title)
	}

	if m := citationJournalRE.FindStringSubmatch(html); m != nil {
		journal = m[1]
		if journal == "" {
			journal = m[2]
		}
		journal = strings.TrimSpace(journal)
	}
	return authors, title, journal
}

// EnrichMetadata fetches citation_author and citation_title meta tags
// for every peer-reviewed/preprint source in the registry and populates
// SourceRef.Authors (and Title, when the stored title is a useless
// placeholder like "Article", "Download", or just the URL). Runs with
// bounded concurrency (8 workers) and a hard ctx deadline; silent on
// per-URL failures — the formatter's "AUTHORS UNAVAILABLE" marker is
// the fallback.
//
// Call this once after all Register/Absorb calls are complete and
// before FormatBibliography — enrichment is a post-registration batch
// step, not part of the hot path.
func (sr *SourceRegistry) EnrichMetadata(ctx context.Context) {
	sr.mu.Lock()
	candidates := make([]int, 0, len(sr.refs))
	for i, ref := range sr.refs {
		if ref.Authors != "" && ref.Journal != "" {
			continue // already fully enriched (e.g., via Absorb from an enriched source)
		}
		tag := ClassifySource(ref.URL)
		if tag != "peer-reviewed" && tag != "preprint" {
			continue // skip non-academic domains — meta tags usually absent
		}
		candidates = append(candidates, i)
	}
	sr.mu.Unlock()

	if len(candidates) == 0 {
		return
	}

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, idx := range candidates {
		sr.mu.Lock()
		rawURL := sr.refs[idx].URL
		currentTitle := sr.refs[idx].Title
		sr.mu.Unlock()

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, rawURL, currentTitle string) {
			defer wg.Done()
			defer func() { <-sem }()
			authors, title, journal := fetchCitationMetadata(ctx, rawURL)
			if authors == "" && title == "" && journal == "" {
				return
			}
			sr.mu.Lock()
			defer sr.mu.Unlock()
			if idx >= len(sr.refs) {
				return
			}
			if authors != "" {
				sr.refs[idx].Authors = authors
			}
			if title != "" && isPlaceholderTitle(currentTitle, rawURL) {
				sr.refs[idx].Title = title
			}
			if journal != "" {
				sr.refs[idx].Journal = journal
			}
		}(idx, rawURL, currentTitle)
	}
	wg.Wait()
}

// isPlaceholderTitle reports whether the stored title is a useless
// placeholder — the scraped page title didn't yield a real article
// title, so we got "Article", "Download", "Home", the bare URL, or
// similar. When true, EnrichMetadata may overwrite with the fetched
// citation_title.
func isPlaceholderTitle(title, rawURL string) bool {
	t := strings.TrimSpace(title)
	if t == "" || t == rawURL {
		return true
	}
	lower := strings.ToLower(t)
	placeholders := []string{
		"article", "download", "pdf", "home", "full text", "full",
		"abstract", "view", "index", "page", "login",
	}
	for _, p := range placeholders {
		if lower == p {
			return true
		}
	}
	return false
}

// Source index line formats used for parsing legacy text-based indices.
var (
	// [N] [Title](URL) — tagged markdown format (title may have nested brackets)
	taggedSrcRE = regexp.MustCompile(`(?m)^\[(\d+)\]\s*\[((?:[^\[\]]|\[[^\]]*\])*)\]\((https?://[^\)]+)\)`)
	// [N] [tag] Title - URL or [N] Title - URL — tagged bibliography format
	plainSrcRE = regexp.MustCompile(`(?m)^\[(\d+)\]\s*(?:\[[^\]]*\]\s*)?(.+?)\s*-\s*(https?://\S+)`)
	// N. Title\n[URL](URL) — two-line persisted format
	persistedSrcRE = regexp.MustCompile(`(?m)^(\d+)\.\s+(.*)\r?\n\[[^\]]+\]\((https?://[^\)]+)\)`)
)

// ParseSourceIndex parses a text block containing source lines in any
// of the known formats and returns structured entries. Used as a
// fallback when structured SourceIndex data is not available.
func ParseSourceIndex(text string) []NumberedSource {
	seen := make(map[int]bool)
	var entries []NumberedSource

	// Try tagged markdown format first: [N] [Title](URL)
	for _, m := range taggedSrcRE.FindAllStringSubmatch(text, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n > 0 && !seen[n] {
			seen[n] = true
			entries = append(entries, NumberedSource{N: n, Title: strings.TrimSpace(m[2]), URL: m[3]})
		}
	}

	// Try plain format: [N] Title - URL
	for _, m := range plainSrcRE.FindAllStringSubmatch(text, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n > 0 && !seen[n] {
			seen[n] = true
			entries = append(entries, NumberedSource{N: n, Title: CleanSourceTitle(strings.TrimSpace(m[2]), m[3]), URL: m[3]})
		}
	}

	// Try persisted two-line format: N. Title\n[URL](URL)
	for _, m := range persistedSrcRE.FindAllStringSubmatch(text, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n > 0 && !seen[n] {
			seen[n] = true
			entries = append(entries, NumberedSource{N: n, Title: strings.TrimSpace(m[2]), URL: m[3]})
		}
	}

	return entries
}
