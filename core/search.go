package core

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ResponseText returns the response content, falling back to reasoning
// for thinking models that put everything in the reasoning field.
// When using reasoning, extracts the final answer (last paragraph)
// since thinking models place their answer after their deliberation.
func ResponseText(resp *Response) string {
	if text := strings.TrimSpace(resp.Content); text != "" {
		return text
	}

	reasoning := strings.TrimSpace(resp.Reasoning)
	if reasoning == "" {
		return ""
	}

	paragraphs := strings.Split(reasoning, "\n\n")
	for i := len(paragraphs) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paragraphs[i])
		if p != "" {
			return p
		}
	}
	return reasoning
}

// StripCodeFence removes markdown code fences (```json ... ```) from LLM output.
func StripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	raw := s
	// Skip the opening fence line (e.g. ```json\n).
	if idx := strings.Index(raw[3:], "\n"); idx >= 0 {
		raw = raw[3+idx+1:]
	}
	if idx := strings.LastIndex(raw, "```"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

// ParseLabel checks whether line starts with "PREFIX:" (case-insensitive)
// and returns the trimmed value after the colon. Returns ("", false) if
// the line doesn't match.
func ParseLabel(line, prefix string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(line))
	tag := strings.ToUpper(prefix)
	if !strings.HasSuffix(tag, ":") {
		tag += ":"
	}
	if strings.HasPrefix(upper, tag) {
		return strings.TrimSpace(strings.TrimSpace(line)[len(tag):]), true
	}
	return "", false
}

// DecodeJSON extracts and unmarshals JSON from LLM output into dest.
// It strips markdown code fences, locates the outermost JSON object
// or array, then removes common LLM-induced invalid-JSON artifacts
// (comment lines, trailing commas) before decoding. Returns an error
// if no valid JSON is found.
func DecodeJSON(text string, dest interface{}) error {
	raw := StripCodeFence(strings.TrimSpace(text))

	// Find the outermost JSON structure.
	objStart := strings.Index(raw, "{")
	arrStart := strings.Index(raw, "[")
	start := -1
	var end int
	if objStart >= 0 && (arrStart < 0 || objStart < arrStart) {
		start = objStart
		end = strings.LastIndex(raw, "}") + 1
	} else if arrStart >= 0 {
		start = arrStart
		end = strings.LastIndex(raw, "]") + 1
	}
	if start >= 0 && end > start {
		raw = raw[start:end]
	}

	raw = sanitizeLLMJSON(raw)
	return json.Unmarshal([]byte(raw), dest)
}

// sanitizeLLMJSON removes three common invalid-JSON patterns that LLMs
// produce despite being asked for strict JSON: (1) whole-line comments
// that start with # or // (often markdown-style "## Note: ..." inserted
// between array elements), (2) trailing commas before } or ], and (3)
// stray "/*...*/" block comments. Each is a regex-free pass so the
// function stays cheap and predictable.
func sanitizeLLMJSON(raw string) string {
	// Strip inline /* ... */ block comments.
	for {
		i := strings.Index(raw, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(raw[i:], "*/")
		if j < 0 {
			break
		}
		raw = raw[:i] + raw[i+j+2:]
	}

	// Strip whole-line comments. A "comment line" is any line whose
	// first non-whitespace character is # or //. Keeps lines that
	// happen to contain # inside string values unchanged.
	var b strings.Builder
	b.Grow(len(raw))
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	raw = b.String()

	// Remove trailing commas before closing ] or } — Python/JS-style
	// that invalid-JSON writers sometimes emit.
	fix := func(s, pattern string) string {
		for {
			i := strings.Index(s, pattern)
			if i < 0 {
				return s
			}
			// pattern is ",<ws>]" or ",<ws>}" — collapse to "]"/"}".
			s = s[:i] + s[i+len(pattern)-1:]
		}
	}
	for _, suffix := range []string{"]", "}"} {
		for _, ws := range []string{"", " ", "\n", "\t", " \n", "\n "} {
			raw = fix(raw, ","+ws+suffix)
		}
	}
	return raw
}

// DecodeJSONList tries to decode a JSON string array from LLM output.
// If JSON parsing fails, it falls back to extracting items from a
// numbered/bulleted list (stripping leading "1. ", "- ", quotes, etc.).
// Items shorter than 10 or longer than 200 characters are skipped.
// The result is capped at maxItems (0 = unlimited).
func DecodeJSONList(text string, maxItems int) []string {
	var items []string
	if DecodeJSON(text, &items) == nil && len(items) > 0 {
		if maxItems > 0 && len(items) > maxItems {
			items = items[:maxItems]
		}
		return items
	}

	// Fallback: extract from numbered/bulleted prose.
	raw := strings.TrimSpace(text)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		for len(line) > 0 && (line[0] >= '0' && line[0] <= '9' || line[0] == '.' || line[0] == '-' || line[0] == ' ') {
			line = line[1:]
		}
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "\"'")
		if len(line) > 10 && len(line) < 200 {
			items = append(items, line)
		}
	}
	if maxItems > 0 && len(items) > maxItems {
		items = items[:maxItems]
	}
	return items
}

// ClassifySource returns a short label describing the type of a source URL
// based on its hostname. Used to tag bibliography entries so readers can see
// at a glance whether a citation comes from a peer-reviewed paper, a vendor
// blog, a Facebook post, etc. Returns "" for unknown/unclassifiable URLs so
// callers can decide whether to omit the tag.
// nonArticlePathPattern catches URLs that are structurally search results,
// listing pages, or boilerplate rather than articles — even when hosted on
// an authoritative domain. A .gov or .edu search-results page shouldn't
// inherit tier-1 classification just because of its hostname; the URL
// path itself is evidence the content isn't a primary document.
//
// Observed in production:
//   - run 489296b0: FTC search page and consumer alert on .gov domain
//   - run f835fdbe: NIST FriendlyUrls_SwitchView with COVID query
//     embedded in ReturnUrl, plus pages.nist.gov DRAFT.html placeholder
//
// The second run revealed that URL-encoded query parameters in
// ReturnUrl-style redirects bypassed the original path-based pattern.
// isNonArticleURL now decodes those redirect parameters before matching
// and also catches SharePoint-style view switchers and DRAFT/template/
// placeholder filename patterns.
var nonArticlePathPattern = regexp.MustCompile(`(?i)(` +
	// Search and result pages
	`/search\b|/search\?|\?q=|&q=|/results\b|/find\b|` +
	// Listing and directory pages
	`/directory\b|/listing\b|/browse\b|/catalog\b|/archive/?$|/index/?$|` +
	// Category and tag pages (match anywhere in path)
	`/category/|/categories/|/tag/|/tags/|/topic/|/topics/|/theme/|/themes/|` +
	// Profile and about pages
	`/profile|/profiles/|/people/|/staff/|/team/|/about/?$|/contact/?$|` +
	// Generic boilerplate
	`/login|/signup|/register|/faq/?$|/help/?$|/sitemap|` +
	// Pagination markers on search-style URLs
	`[?&]page=\d+|` +
	// SharePoint/CMS view switchers and redirect wrappers
	`friendlyurls_switchview|viewswitcher|switchview|changeview|setview|` +
	`\?returnurl=|&returnurl=|\?redir=|&redir=|\?redirect=|&redirect=|` +
	// DRAFT, template, placeholder, and example pages
	`/draft\.html?\b|/draft/?$|/template\.html?\b|/template/?$|` +
	`/placeholder|/example\.html?\b|/example/?$|/boilerplate|` +
	// Generic "untitled" or system-generated stub patterns
	`/untitled|/default\.html?\b` +
	`)`)

// IsNonArticleURL returns true if the URL path structure suggests a
// search results, listing, or boilerplate page rather than a primary
// document. Used by ClassifySource as a downgrade signal and by
// consumers as a hard rejection filter.
//
// Also URL-decodes the query string before matching so that redirect
// wrappers like "/_FriendlyUrls_SwitchView?ReturnUrl=%2F%3Fq%3Daction"
// get caught — the decoded form contains "?q=" which the pattern hits.
func IsNonArticleURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	path := u.Path
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if nonArticlePathPattern.MatchString(path) {
		return true
	}
	// Decode any embedded redirect URL parameters. A pattern like
	// ReturnUrl=%2F%3Fq%3D... decodes to ReturnUrl=/?q=... which
	// reveals the search-query nature of the target.
	if u.RawQuery != "" {
		if decoded, derr := url.QueryUnescape(u.RawQuery); derr == nil && decoded != u.RawQuery {
			if nonArticlePathPattern.MatchString(decoded) {
				return true
			}
		}
	}
	return false
}

func ClassifySource(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))

	// Non-article paths (search results, listings, boilerplate) get
	// downgraded to "web" regardless of hostname. A .gov search page
	// is structurally not a primary document.
	if IsNonArticleURL(rawURL) {
		return "web"
	}

	// Preprint repositories.
	switch {
	case host == "arxiv.org" || strings.HasSuffix(host, ".arxiv.org"):
		return "preprint"
	case host == "biorxiv.org" || host == "medrxiv.org" || host == "ssrn.com" || host == "papers.ssrn.com":
		return "preprint"
	}

	// Peer-reviewed publishers and academic indexes.
	peerHosts := []string{
		"pmc.ncbi.nlm.nih.gov", "ncbi.nlm.nih.gov", "pubmed.ncbi.nlm.nih.gov",
		"nature.com", "science.org", "sciencedirect.com", "cell.com",
		"link.springer.com", "springer.com", "wiley.com", "onlinelibrary.wiley.com",
		"cambridge.org", "oup.com", "academic.oup.com", "tandfonline.com",
		"jstor.org", "ieeexplore.ieee.org", "dl.acm.org", "mdpi.com",
		"frontiersin.org", "plos.org", "journals.plos.org", "thelancet.com",
		"nejm.org", "bmj.com", "annualreviews.org", "researchgate.net",
		"semanticscholar.org",
		// Top ML / CS conference venues — peer-reviewed proceedings.
		"aaai.org", "neurips.cc", "proceedings.neurips.cc", "papers.nips.cc",
		"proceedings.mlr.press", "openreview.net", "aclanthology.org",
		"jmlr.org", "icml.cc", "iclr.cc",
	}
	for _, h := range peerHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "peer-reviewed"
		}
	}

	// Government and intergovernmental organizations.
	switch {
	case strings.HasSuffix(host, ".gov"), strings.HasSuffix(host, ".mil"):
		return "gov"
	case host == "un.org" || strings.HasSuffix(host, ".un.org"):
		return "gov"
	case host == "who.int" || strings.HasSuffix(host, ".who.int"):
		return "gov"
	case host == "europa.eu" || strings.HasSuffix(host, ".europa.eu"):
		return "gov"
	case host == "icrc.org" || strings.HasSuffix(host, ".icrc.org"):
		return "ngo"
	}

	// Academic institutions (.edu).
	if strings.HasSuffix(host, ".edu") || strings.HasSuffix(host, ".ac.uk") || strings.HasSuffix(host, ".edu.au") {
		return "edu"
	}

	// Social media (true social signals — short-form, follower-driven).
	socialHosts := []string{
		"facebook.com", "twitter.com", "x.com", "linkedin.com",
		"reddit.com", "tiktok.com", "instagram.com", "youtube.com",
	}
	for _, h := range socialHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "social"
		}
	}

	// Long-form blogging platforms — author-driven articles, not social.
	blogPlatformHosts := []string{"medium.com", "substack.com"}
	for _, h := range blogPlatformHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "blog"
		}
	}

	// Press release wires.
	prHosts := []string{"prnewswire.com", "businesswire.com", "globenewswire.com", "newswire.com", "einpresswire.com"}
	for _, h := range prHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "press release"
		}
	}

	// Major news outlets — well-known, generally fact-checked.
	newsHosts := []string{
		"nytimes.com", "washingtonpost.com", "reuters.com", "apnews.com",
		"bbc.com", "bbc.co.uk", "theguardian.com", "ft.com", "wsj.com",
		"bloomberg.com", "economist.com", "npr.org", "pbs.org",
		"cnn.com", "axios.com", "politico.com", "propublica.org",
		"theatlantic.com", "newyorker.com", "wired.com", "arstechnica.com",
	}
	for _, h := range newsHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "news"
		}
	}

	// Commentary tier — ideologically-charged outlets, policy think
	// tanks, and industry legal commentary that produce substantive
	// work but shouldn't stand alone as evidence for load-bearing
	// claims. Synthesis rules require at least one primary-tier source
	// (peer-reviewed / gov / edu / preprint) to co-support any claim
	// that cites a commentary-tier source. Kept bipartisan on purpose
	// so the filter is epistemic, not ideological.
	commentaryHosts := []string{
		// Cable news opinion / partisan outlets.
		"foxnews.com", "breitbart.com", "dailywire.com",
		// Ideologically-charged magazines (left and right).
		"jacobin.com", "nationalreview.com", "reason.com",
		// Policy think tanks (left and right for parity).
		"rooseveltinstitute.org", "heritage.org",
		"cato.org", "mercatus.org", "epi.org",
		"americanprogress.org", "aei.org", "urban.org",
		"manhattan-institute.org",
		// (brookings.edu intentionally omitted — the .edu check above
		// catches it first as an academic institution; leaving it at
		// edu-tier reflects its mixed research-and-policy profile.)
		// Industry-legal / patent commentary.
		"ipwatchdog.com",
	}
	for _, h := range commentaryHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return "commentary"
		}
	}

	// Wikipedia and reference works.
	if host == "wikipedia.org" || strings.HasSuffix(host, ".wikipedia.org") {
		return "wiki"
	}

	// Vendor/marketing fallback heuristic — paths containing /blog/ on
	// commercial-looking domains. This is a soft signal, not authoritative.
	if strings.Contains(strings.ToLower(u.Path), "/blog/") {
		return "blog"
	}

	return "web"
}

// (IsWeakSource moved to core/filters.go — distinct concern from
// search dispatch and classification.)

// CleanSourceTitle strips bracket prefixes like [PDF] from source titles.
// If the title is empty, a URL, or a hash/ID, generates a readable title from the URL.
func CleanSourceTitle(title, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = fallback
	}
	for strings.HasPrefix(title, "[") {
		if end := strings.Index(title, "]"); end >= 0 {
			title = strings.TrimSpace(title[end+1:])
		} else {
			break
		}
	}
	if title == "" {
		title = fallback
	}

	// Detect hash/ID-like titles (hex strings, PMC IDs, etc.) and treat as unreadable.
	if IsUnreadableTitle(title) && fallback != title {
		title = fallback
	}

	// If the title is still a URL, generate a readable name from domain + path.
	if strings.HasPrefix(title, "http://") || strings.HasPrefix(title, "https://") {
		if u, err := url.Parse(title); err == nil {
			domain := strings.TrimPrefix(u.Hostname(), "www.")
			path := strings.Trim(u.Path, "/")
			if path != "" {
				// Use last meaningful path segment.
				parts := strings.Split(path, "/")
				last := parts[len(parts)-1]
				last = strings.TrimSuffix(last, ".html")
				last = strings.TrimSuffix(last, ".htm")
				last = strings.TrimSuffix(last, ".pdf")
				last = strings.ReplaceAll(last, "-", " ")
				last = strings.ReplaceAll(last, "_", " ")
				if len(last) > 60 {
					last = last[:60]
				}
				// If the cleaned segment is still unreadable, try earlier segments.
				if IsUnreadableTitle(last) {
					for i := len(parts) - 2; i >= 0; i-- {
						candidate := strings.ReplaceAll(parts[i], "-", " ")
						candidate = strings.ReplaceAll(candidate, "_", " ")
						if !IsUnreadableTitle(candidate) && len(candidate) > 3 {
							last = candidate
							break
						}
					}
				}
				if IsUnreadableTitle(last) {
					title = domain
				} else {
					title = strings.Title(last) + " - " + domain
				}
			} else {
				title = domain
			}
		}
	}
	return title
}

// isUnreadableTitle returns true if the string looks like a hash, ID, or code rather than readable text.
func IsUnreadableTitle(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return true
	}
	// Count character types.
	digits := 0
	letters := 0
	spaces := 0
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			letters++
		case c == ' ':
			spaces++
		}
	}
	total := len(s)

	// Pure numeric (PubMed IDs like "35868169").
	if digits > 0 && letters == 0 && spaces == 0 {
		return true
	}
	// DOI-style fragments (S41467 022 34769 6) — mostly digits with few letters.
	if total > 5 && float64(digits)/float64(total) > 0.5 && spaces <= 4 {
		return true
	}
	// Hex-like hash (D27B1681466712B6F784AEED1A745979).
	if total >= 16 {
		hexChars := 0
		for _, c := range s {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
				hexChars++
			}
		}
		if float64(hexChars)/float64(total) > 0.8 {
			return true
		}
	}
	// Too short with no real words (e.g., "Full", "PMC12345").
	if spaces == 0 && total < 8 && digits > 0 {
		return true
	}
	return false
}

// ExtractSummary pulls the "SUMMARY: ..." line from the end of a response
// and returns (body_without_summary, summary_line).
func ExtractSummary(text string) (string, string) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(strings.ToUpper(trimmed), "SUMMARY:") {
			summary := strings.TrimSpace(trimmed[len("SUMMARY:"):])
			body := strings.TrimSpace(strings.Join(lines[:i], "\n"))
			return body, summary
		}
	}
	return text, ""
}

// WebSearchAvailable checks if the web_search tool is registered.
func WebSearchAvailable() bool {
	tools, err := GetAgentTools("web_search")
	return err == nil && len(tools) > 0
}

// WebSearch runs a web search query using the registered web_search tool.
func WebSearch(query string) string {
	tools, err := GetAgentTools("web_search")
	if err != nil || len(tools) == 0 {
		return ""
	}
	result, err := tools[0].Handler(map[string]any{"query": query})
	if err != nil {
		Debug("web search failed: %s", err)
		return ""
	}
	if result == "" || result == "No results found." {
		return ""
	}
	return result
}

// SearchCache provides thread-safe in-memory caching of search results.
// Uses in-flight tracking to prevent duplicate API calls when multiple
// goroutines search for the same query concurrently.
type SearchCache struct {
	mu       sync.Mutex
	cache    map[string]string
	inflight map[string]*searchFlight
	apiCalls int
}

// searchFlight tracks an in-progress search so concurrent callers wait
// for the same result instead of hitting the API twice.
type searchFlight struct {
	done chan struct{}
	result string
}

// NewSearchCache creates a new empty search cache.
func NewSearchCache() *SearchCache {
	return &SearchCache{
		cache:    make(map[string]string),
		inflight: make(map[string]*searchFlight),
	}
}

// get retrieves a cached result or returns an in-flight tracker to wait on.
// Returns (result, true) on cache hit, ("", false) on miss.
// On miss, registers an in-flight entry and returns it — caller must
// call set() when done to unblock other waiters.
func (c *SearchCache) get(key string) (string, *searchFlight, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	c.mu.Lock()
	defer c.mu.Unlock()

	if v, ok := c.cache[key]; ok {
		return v, nil, true
	}
	if f, ok := c.inflight[key]; ok {
		return "", f, false
	}
	f := &searchFlight{done: make(chan struct{})}
	c.inflight[key] = f
	return "", nil, false
}

// set stores a result and unblocks any goroutines waiting for this key.
func (c *SearchCache) set(key string, result string) {
	key = strings.ToLower(strings.TrimSpace(key))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = result
	if f, ok := c.inflight[key]; ok {
		f.result = result
		close(f.done)
		delete(c.inflight, key)
	}
}

// Get retrieves a cached result. Returns the value and whether it was found.
func (c *SearchCache) Get(key string) (string, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key]
	return v, ok
}

// Set stores a result in the cache.
func (c *SearchCache) Set(key string, result string) {
	c.set(key, result)
}

// incAPI increments the API call counter and returns the new count.
func (c *SearchCache) incAPI() int {
	c.mu.Lock()
	c.apiCalls++
	n := c.apiCalls
	c.mu.Unlock()
	return n
}

// APICalls returns the total number of search API calls made through this cache.
func (c *SearchCache) APICalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apiCalls
}

// CachedSearch runs a web search with caching.
// Concurrent calls for the same query share a single API call.
func CachedSearch(cache *SearchCache, query string) string {
	result, flight, hit := cache.get(query)
	if hit {
		Debug("search cache hit for: %s", query)
		return result
	}
	if flight != nil {
		Debug("search waiting for in-flight: %s", query)
		<-flight.done
		return flight.result
	}
	n := cache.incAPI()
	Debug("[search] API call #%d: %s", n, query)
	result = WebSearch(query)
	cache.set(query, result)
	return result
}

// CachedCrossSearch runs a cross-provider search with caching.
// Falls back to single-provider search if cross-provider fails.
// Uses a unified cache key so cross and single-provider results
// are shared, preventing duplicate API calls.
func CachedCrossSearch(cache *SearchCache, query string) string {
	result, flight, hit := cache.get(query)
	if hit {
		Debug("cross-search cache hit for: %s", query)
		return result
	}
	if flight != nil {
		Debug("cross-search waiting for in-flight: %s", query)
		<-flight.done
		return flight.result
	}
	n := cache.incAPI()
	Debug("[search] API call #%d (cross): %s", n, query)
	start := time.Now()
	if CrossSearchFunc != nil {
		result, err := CrossSearchFunc(query)
		if err != nil {
			Debug("[search] cross-search failed in %s: %v", time.Since(start).Round(time.Millisecond), err)
		} else {
			Debug("[search] cross-search completed in %s: %s", time.Since(start).Round(time.Millisecond), query)
		}
		cache.set(query, result)
		return result
	}
	result = WebSearch(query)
	Debug("[search] search completed in %s: %s", time.Since(start).Round(time.Millisecond), query)
	cache.set(query, result)
	return result
}

// ConsensusSearchResult holds the query and results from one consensus search.
type ConsensusSearchResult struct {
	Query   string
	Results string
}

// RunConsensusSearches executes three parallel searches for expert consensus:
// (1) expert opinion / scientific consensus, (2) systematic reviews / evidence,
// (3) fact-check sites. Returns the individual results and the combined text.
// jurisdictionTag is prepended to queries (can be empty).
func RunConsensusSearches(cache *SearchCache, shortTopic string, jurisdictionTag string) ([]ConsensusSearchResult, string) {
	sr := make([]ConsensusSearchResult, 3)
	jt := ""
	if jurisdictionTag != "" {
		jt = " " + jurisdictionTag
	}
	q1 := fmt.Sprintf("%s%s expert consensus scholarly opinion %d", shortTopic, jt, time.Now().Year())
	q2 := fmt.Sprintf("%s%s arguments evidence analysis review %d", shortTopic, jt, time.Now().Year())
	q3 := fmt.Sprintf("%s%s site:snopes.com OR site:politifact.com OR site:factcheck.org OR site:apnews.com OR site:plato.stanford.edu fact check", shortTopic, jt)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); sr[0] = ConsensusSearchResult{Query: q1, Results: CachedCrossSearch(cache, q1)} }()
	go func() { defer wg.Done(); sr[1] = ConsensusSearchResult{Query: q2, Results: CachedCrossSearch(cache, q2)} }()
	go func() { defer wg.Done(); sr[2] = ConsensusSearchResult{Query: q3, Results: CachedSearch(cache, q3)} }()
	wg.Wait()

	var combined string
	for _, s := range sr {
		if s.Results != "" {
			if combined != "" {
				combined += "\n\n"
			}
			combined += s.Results
		}
	}
	return sr, combined
}

// ExtractJurisdiction detects jurisdiction references in text
// (e.g., "per California law", "in the EU", "under Texas statute").
// Returns a short jurisdiction tag for search queries, or empty string.
func ExtractJurisdiction(text string) string {
	lower := strings.ToLower(text)
	states := []struct{ name, abbr string }{
		{"california", "California"}, {"texas", "Texas"}, {"new york", "New York"},
		{"florida", "Florida"}, {"new hampshire", "New Hampshire"}, {"ohio", "Ohio"},
		{"illinois", "Illinois"}, {"pennsylvania", "Pennsylvania"}, {"georgia", "Georgia"},
		{"michigan", "Michigan"}, {"new jersey", "New Jersey"}, {"virginia", "Virginia"},
		{"washington", "Washington"}, {"arizona", "Arizona"}, {"massachusetts", "Massachusetts"},
		{"colorado", "Colorado"}, {"oregon", "Oregon"}, {"nevada", "Nevada"},
		{"minnesota", "Minnesota"}, {"wisconsin", "Wisconsin"}, {"maryland", "Maryland"},
		{"tennessee", "Tennessee"}, {"indiana", "Indiana"}, {"missouri", "Missouri"},
		{"connecticut", "Connecticut"}, {"iowa", "Iowa"}, {"louisiana", "Louisiana"},
		{"kentucky", "Kentucky"}, {"oklahoma", "Oklahoma"}, {"alabama", "Alabama"},
		{"south carolina", "South Carolina"}, {"north carolina", "North Carolina"},
	}
	for _, s := range states {
		if strings.Contains(lower, s.name) {
			return s.abbr
		}
	}
	abbrs := map[string]string{
		" ca ": "California", " tx ": "Texas", " ny ": "New York", " fl ": "Florida",
		" nh ": "New Hampshire", " oh ": "Ohio", " il ": "Illinois", " pa ": "Pennsylvania",
		" nj ": "New Jersey", " va ": "Virginia", " az ": "Arizona", " ma ": "Massachusetts",
		" co ": "Colorado", " or ": "Oregon", " nv ": "Nevada", " md ": "Maryland",
	}
	padded := " " + lower + " "
	for abbr, full := range abbrs {
		if strings.Contains(padded, abbr) {
			return full
		}
	}
	regions := []struct{ pattern, label string }{
		{"united kingdom", "UK"}, {" uk ", "UK"}, {"european union", "EU"},
		{" eu ", "EU"}, {"canada", "Canada"}, {"australia", "Australia"},
		{"germany", "Germany"}, {"france", "France"}, {"japan", "Japan"},
		{"china", "China"}, {"india", "India"}, {"brazil", "Brazil"},
		{"federal", "US federal"},
	}
	for _, r := range regions {
		if strings.Contains(padded, r.pattern) {
			return r.label
		}
	}
	return ""
}

// CrossSearchFunc is set by the websearch package to provide cross-provider search.
var CrossSearchFunc func(query string) (string, error)

// IsLowQualitySource returns true if a URL or title indicates a source that
// should be excluded from research — municipal utility pages, social media,
// API endpoints, template repos, video platforms, shopping sites, etc.
func IsLowQualitySource(url, title string) bool {
	u := strings.ToLower(url)
	t := strings.ToLower(title)

	// Social media and video platforms.
	for _, domain := range []string{
		"youtube.com", "youtu.be", "tiktok.com", "instagram.com",
		"facebook.com", "twitter.com", "x.com", "reddit.com",
		"linkedin.com/posts", "threads.net",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	// Code hosting and templates.
	if strings.Contains(u, "github.com") && !strings.Contains(u, "github.io") {
		return true
	}

	// API endpoints and machine-readable pages.
	for _, pattern := range []string{
		"/api/", "/api?", "swagger", "openapi",
		"/feed", "/rss", "/sitemap", "/robots.txt",
	} {
		if strings.Contains(u, pattern) {
			return true
		}
	}

	// Municipal utility pages (parking, permits, traffic regulations).
	for _, pattern := range []string{
		"/parking", "/permits", "/traffic-regulations",
		"/traffic_orders", "/zoning-map",
	} {
		if strings.Contains(u, pattern) {
			// Allow if title suggests analysis rather than a utility page.
			if strings.Contains(t, "study") || strings.Contains(t, "report") ||
				strings.Contains(t, "analysis") || strings.Contains(t, "impact") ||
				strings.Contains(t, "research") {
				continue
			}
			return true
		}
	}

	// Job listing sites.
	for _, domain := range []string{
		"indeed.com", "glassdoor.com", "ziprecruiter.com",
		"linkedin.com/jobs", "monster.com", "careerbuilder.com",
		"jobtoday.com",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	// Pirate/mirror book sites.
	for _, domain := range []string{
		"dokumen.pub", "libgen.", "sci-hub.", "z-lib.",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	// Career guide and program pages (not research).
	for _, domain := range []string{
		"careers.usnews.com",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	// Shopping and product pages.
	for _, domain := range []string{
		"amazon.com", "ebay.com", "walmart.com", "etsy.com",
		"aliexpress.com", "shopify.com",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	// High-school and student newspaper sites. Common URL patterns include
	// ".highschool." (rare but present), school-district subdomains that
	// embed "hs" or "highschool", and title phrases that signal a student
	// publication. We match conservatively on title phrases tied to a .com
	// or .org host; .edu is left alone because edu covers university
	// research sources we do want to keep.
	highSchoolTitlePhrases := []string{
		"student newspaper", "high school newspaper", "school newspaper",
		"student publication", "student journalist",
	}
	if !strings.Contains(u, ".edu") {
		for _, phrase := range highSchoolTitlePhrases {
			if strings.Contains(t, phrase) {
				return true
			}
		}
	}
	// Known high-school / student-paper hosts. These show up in web search
	// for any US-politics topic because student papers cover elections.
	// Heuristic: subdomain or path fragments like "periscope" (common
	// student-paper name), "thelion", "theoracle", "gazette" attached to
	// a generic .com are weak signals on their own, but specific known
	// hosts are safe to block.
	for _, domain := range []string{
		"chsperiscope.com",        // Chagrin Falls HS
		"wsspaper.com",            // West Springfield HS
		"thepeaksunset.com",       // Mt. Si HS
		"thepowelltribune.com/category/student",
	} {
		if strings.Contains(u, domain) {
			return true
		}
	}

	return false
}
