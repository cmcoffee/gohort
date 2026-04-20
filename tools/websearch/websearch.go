package websearch

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/apiclient"
)

func init() {
	RegisterChatTool(new(WebSearchTool))
	RegisterChatTool(new(FetchURLTool))
	CrossSearchFunc = CrossProviderSearch
}

// Source represents a single search result with title, URL, and snippet.
type Source struct {
	Title   string `json:"Title"`
	URL     string `json:"URL"`
	Snippet string `json:"Snippet,omitempty"`
}

// ParseSearchResults parses numbered search results into Source structs.
// Expected format: "N. Title\n   URL\n   Snippet" separated by blank lines.
func ParseSearchResults(results string) []Source {
	var sources []Source
	blocks := strings.Split(results, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		title := strings.TrimSpace(lines[0])
		if idx := strings.Index(title, ". "); idx >= 0 && idx < 4 {
			title = title[idx+2:]
		}
		u := strings.TrimSpace(lines[1])
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		var snippet string
		if len(lines) > 2 {
			snippet = strings.TrimSpace(strings.Join(lines[2:], " "))
		}
		sources = append(sources, Source{Title: title, URL: u, Snippet: snippet})
	}
	return sources
}

// DomainFromURL extracts the hostname from a URL string, stripping www. prefix.
func DomainFromURL(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		return strings.TrimPrefix(parsed.Hostname(), "www.")
	}
	return ""
}

// isLowValueURL returns true if the URL likely points to a non-article page
// (profiles, directories, about pages, tag listings, etc.)
func isLowValueURL(u string) bool {
	lower := strings.ToLower(u)
	// Non-article page patterns.
	for _, seg := range []string{
		"/fellows/", "/fellow/", "/staff/", "/people/", "/profile/",
		"/profiles/", "/directory/", "/about/", "/about-us",
		"/team/", "/author/", "/authors/", "/bio/",
		"/tag/", "/tags/", "/category/", "/categories/",
		"/search?", "/login", "/signup", "/subscribe",
		"/contact", "/careers", "/jobs/",
	} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	// Product review/affiliate patterns.
	for _, pattern := range []string{
		"-reviews-", "/reviews/", "product-review",
		"affiliate", "discount-code", "coupon",
		"a-closer-look-at", "honest-review",
		"buy-now", "order-now", "special-offer",
		"/friends-forum/topic/", "/forum/topic/",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// SelectDiverseArticles picks up to n sources, ensuring domain diversity
// and prioritizing higher-credibility domains.
func SelectDiverseArticles(sources []Source, n int) []Source {
	type scored struct {
		source Source
		score  int
	}

	var items []scored
	for _, s := range sources {
		if s.URL == "" {
			continue
		}
		if isLowValueURL(s.URL) {
			continue
		}
		domain := DomainFromURL(s.URL)
		items = append(items, scored{source: s, score: DomainCredibility(domain)})
	}

	// Sort by credibility score descending.
	for i := 0; i < len(items); i++ {
		best := i
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[best].score {
				best = j
			}
		}
		items[i], items[best] = items[best], items[i]
	}

	// Pick top n with domain diversity.
	var selected []Source
	seen_domains := make(map[string]int)
	for _, item := range items {
		if len(selected) >= n {
			break
		}
		domain := DomainFromURL(item.source.URL)
		if seen_domains[domain] >= 1 {
			continue
		}
		seen_domains[domain]++
		selected = append(selected, item.source)
	}

	return selected
}

// WebSearchTool performs web searches using the configured provider.
type WebSearchTool struct{}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Desc() string {
	return "Search the web for information. Returns a list of results with titles, URLs, and snippets."
}

func (t *WebSearchTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {Type: "string", Description: "The search query."},
	}
}

func (t *WebSearchTool) Run(args map[string]any) (string, error) {
	query := StringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("'query' is required")
	}

	cfg := LoadWebSearchConfig()
	provider := cfg.Provider
	if provider == "" {
		provider = "duckduckgo"
	}

	result, err := SearchWithProvider(query, provider, cfg.APIKey, cfg.Endpoint)
	if err == nil && result != "" && result != "No results found." {
		Debug("[web_search] results:\n%s", result)
	}
	return result, err
}

// FetchURLTool is the chat tool for reading the full text of a specific
// URL. Used as the natural follow-up to web_search — the LLM gets
// snippets from a search, identifies a promising URL, then fetches
// that page's readable text to answer in detail. Also useful when the
// user pastes a URL directly and asks for a summary.
//
// HTML is stripped; only readable text is returned, capped at 8000
// characters to keep the chat context manageable. Loopback and private
// addresses are rejected to prevent SSRF — this tool reaches the live
// web, not internal infrastructure.
type FetchURLTool struct{}

func (t *FetchURLTool) Name() string { return "fetch_url" }
func (t *FetchURLTool) Desc() string {
	return "Fetch the readable text content of a specific URL from the live web. Use after web_search returns a URL whose content you want to read in full, or when the user pastes a URL and asks you to read or summarize it. Returns up to 8000 characters of extracted text. Strips HTML, scripts, ads."
}

func (t *FetchURLTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "The URL to fetch. Must be http:// or https://."},
	}
}

func (t *FetchURLTool) Run(args map[string]any) (string, error) {
	target := StringArg(args, "url")
	if target == "" {
		return "", fmt.Errorf("'url' is required")
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("'url' must be an http:// or https:// URL")
	}
	// SSRF guard: refuse loopback + private-network hosts so the tool
	// can't be used to probe the server's own internal services.
	if host := parsed.Hostname(); host != "" {
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
			}
		}
		if lower := strings.ToLower(host); lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
			return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
		}
	}
	text, err := FetchArticle(target, 8000)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "Fetched successfully but the page has no readable text (likely JavaScript-heavy or empty).", nil
	}
	Debug("[fetch_url] %s → %d chars", target, len(text))
	return fmt.Sprintf("Fetched %s (%d chars):\n\n%s", target, len(text), text), nil
}

// SearchWithProvider runs a search query using a specific provider.
// This is exported so callers can run cross-provider searches.
func SearchWithProvider(query string, provider string, apiKey string, endpoint string) (string, error) {
	Debug("[web_search] provider=%s query=%q", provider, query)
	var result string
	var err error
	switch provider {
	case "duckduckgo":
		result, err = searchDuckDuckGo(query)
	case "brave":
		result, err = searchBrave(query, apiKey)
	case "google":
		result, err = searchGoogle(query, apiKey)
	case "searxng":
		result, err = searchSearXNG(query, endpoint)
	case "serper":
		result, err = searchSerper(query, apiKey)
	default:
		return "", fmt.Errorf("unknown search provider: %s", provider)
	}
	if err != nil {
		Debug("[web_search] error: %s", err)
	} else if result == "" || result == "No results found." {
		Debug("[web_search] no results")
	}
	return result, err
}

// CrossProviderSearch runs a search query using the configured provider.
func CrossProviderSearch(query string) (string, error) {
	cfg := LoadWebSearchConfig()
	provider := cfg.Provider
	if provider == "" {
		provider = "duckduckgo"
	}

	return SearchWithProvider(query, provider, cfg.APIKey, cfg.Endpoint)
}

// searchDuckDuckGo scrapes DuckDuckGo's lite HTML interface (no API key needed).
func searchDuckDuckGo(query string) (string, error) {
	client := &apiclient.APIClient{
		Server:         "lite.duckduckgo.com",
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}

	form_data := url.Values{"q": {query}}.Encode()

	req, err := client.NewRequest("POST", "/lite/")
	if err != nil {
		return "", fmt.Errorf("duckduckgo request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Body = io.NopCloser(bytes.NewReader([]byte(form_data)))
	req.ContentLength = int64(len(form_data))

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("duckduckgo request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	return parseDDGLite(string(body)), nil
}

// parseDDGLite extracts search results from DuckDuckGo Lite HTML.
func parseDDGLite(html string) string {
	var results []string
	count := 0

	// DuckDuckGo lite results are in table rows with class "result-link" for URLs
	// and "result-snippet" for descriptions. Parse simply by finding patterns.
	lines := strings.Split(html, "\n")
	var current_title, current_url, current_snippet string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Extract links: <a rel="nofollow" href="..." class="result-link">Title</a>
		if strings.Contains(trimmed, "class=\"result-link\"") {
			if idx := strings.Index(trimmed, "href=\""); idx >= 0 {
				rest := trimmed[idx+6:]
				if end := strings.Index(rest, "\""); end >= 0 {
					current_url = rest[:end]
				}
			}
			// Extract title text between > and </a>
			if idx := strings.Index(trimmed, ">"); idx >= 0 {
				rest := trimmed[idx+1:]
				if end := strings.Index(rest, "</a>"); end >= 0 {
					current_title = stripTags(rest[:end])
				}
			}
		}

		// Extract snippet: <td class="result-snippet">...</td>
		if strings.Contains(trimmed, "class=\"result-snippet\"") {
			if idx := strings.Index(trimmed, ">"); idx >= 0 {
				rest := trimmed[idx+1:]
				if end := strings.Index(rest, "</td>"); end >= 0 {
					current_snippet = stripTags(rest[:end])
				}
			}

			if current_title != "" && current_url != "" {
				results = append(results, fmt.Sprintf("%d. %s\n   %s\n   %s", count+1, current_title, current_url, current_snippet))
				count++
				current_title = ""
				current_url = ""
				current_snippet = ""
				if count >= 8 {
					break
				}
			}
		}
	}

	if len(results) == 0 {
		return "No results found."
	}
	return strings.Join(results, "\n\n")
}

// stripTags removes HTML tags from a string.
func stripTags(s string) string {
	var out strings.Builder
	in_tag := false
	for _, ch := range s {
		if ch == '<' {
			in_tag = true
		} else if ch == '>' {
			in_tag = false
		} else if !in_tag {
			out.WriteRune(ch)
		}
	}
	return strings.TrimSpace(out.String())
}

// Brave Search API types.
type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

// searchBrave uses the Brave Search API.
func searchBrave(query string, apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("brave search requires an API key (configure with --setup)")
	}

	client := &apiclient.APIClient{
		Server:         "api.search.brave.com",
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}

	req, err := client.NewRequest("GET", "/res/v1/web/search?q="+url.QueryEscape(query)+"&count=20")
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Subscription-Token", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("brave request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("brave api error (%d): %s", resp.StatusCode, string(body))
	}

	var result braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing brave response: %w", err)
	}

	var results []string
	for i, r := range result.Web.Results {
		results = append(results, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.Title, r.URL, r.Description))
	}

	if len(results) == 0 {
		return "No results found.", nil
	}
	return strings.Join(results, "\n\n"), nil
}

// Serper.dev API types.
type serperResponse struct {
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic"`
}

// searchSerper uses the Serper.dev Google Search API.
func searchSerper(query string, apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("serper search requires an API key (configure with --setup)")
	}

	payload, _ := json.Marshal(map[string]any{
		"q":   query,
		"num": 20,
	})

	client := &apiclient.APIClient{
		Server:         "google.serper.dev",
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}

	req, err := client.NewRequest("POST", "/search")
	if err != nil {
		return "", err
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("serper request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("serper api error (%d): %s", resp.StatusCode, string(body))
	}

	var result serperResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing serper response: %w", err)
	}

	var results []string
	for i, r := range result.Organic {
		results = append(results, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.Title, r.Link, r.Snippet))
	}

	if len(results) == 0 {
		return "No results found.", nil
	}
	return strings.Join(results, "\n\n"), nil
}

// Google Custom Search API types.
type googleResponse struct {
	Items []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"items"`
}

// searchGoogle uses the Google Custom Search JSON API.
// API key format should be "key:cx" (API key and custom search engine ID separated by colon).
func searchGoogle(query string, apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("google search requires an API key in 'key:cx' format (configure with --setup)")
	}

	parts := strings.SplitN(apiKey, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("google API key must be in 'key:cx' format (API key and search engine ID separated by ':')")
	}
	key, cx := parts[0], parts[1]

	client := &apiclient.APIClient{
		Server:         "www.googleapis.com",
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}

	path := fmt.Sprintf("/customsearch/v1?key=%s&cx=%s&q=%s&num=10",
		url.QueryEscape(key), url.QueryEscape(cx), url.QueryEscape(query))

	req, err := client.NewRequest("GET", path)
	if err != nil {
		return "", fmt.Errorf("google request failed: %w", err)
	}

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("google request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("google api error (%d): %s", resp.StatusCode, string(body))
	}

	var result googleResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing google response: %w", err)
	}

	var results []string
	for i, r := range result.Items {
		results = append(results, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.Title, r.Link, r.Snippet))
	}

	if len(results) == 0 {
		return "No results found.", nil
	}
	return strings.Join(results, "\n\n"), nil
}

// SearXNG response types.
type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// searchSearXNG uses a SearXNG instance's JSON API.
func searchSearXNG(query string, endpoint string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("searxng requires an endpoint URL (configure with --setup)")
	}

	endpoint = strings.TrimSuffix(endpoint, "/")

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid searxng endpoint: %w", err)
	}

	client := &apiclient.APIClient{
		Server:         parsed.Host,
		URLScheme:      parsed.Scheme,
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}

	path := fmt.Sprintf("%s/search?q=%s&format=json&categories=general", parsed.Path, url.QueryEscape(query))

	req, err := client.NewRequest("GET", path)
	if err != nil {
		return "", fmt.Errorf("searxng request failed: %w", err)
	}

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("searxng request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("searxng error (%d): %s", resp.StatusCode, string(body))
	}

	var result searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parsing searxng response: %w", err)
	}

	var results []string
	for i, r := range result.Results {
		if i >= 8 {
			break
		}
		results = append(results, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.Title, r.URL, r.Content))
	}

	if len(results) == 0 {
		return "No results found.", nil
	}
	return strings.Join(results, "\n\n"), nil
}

// SourceMeta holds metadata extracted from a fetched article.
type SourceMeta struct {
	Title   string // og:title or <title>
	Author  string // author meta tag
	Site    string // og:site_name
	PubDate string // article:published_time, datePublished, etc.
	Domain  string // hostname extracted from URL
}

// FetchArticleWithMeta fetches a URL and returns both readable text and metadata.
func FetchArticleWithMeta(target_url string, max_chars int) (string, SourceMeta, error) {
	text, meta, err := fetchArticleInternal(target_url, max_chars)
	return text, meta, err
}

// FetchArticle fetches a URL and extracts readable text content from the HTML.
// Returns up to max_chars characters of extracted text.
func FetchArticle(target_url string, max_chars int) (string, error) {
	text, _, err := fetchArticleInternal(target_url, max_chars)
	return text, err
}

func fetchArticleInternal(target_url string, max_chars int) (string, SourceMeta, error) {
	var meta SourceMeta
	if target_url == "" {
		return "", meta, fmt.Errorf("empty URL")
	}

	// Extract domain from URL.
	if parsed, err := url.Parse(target_url); err == nil {
		meta.Domain = parsed.Hostname()
	}

	parsed, err := url.Parse(target_url)
	if err != nil {
		return "", meta, fmt.Errorf("parsing URL: %w", err)
	}

	client := &apiclient.APIClient{
		Server:         parsed.Host,
		URLScheme:      parsed.Scheme,
		AgentString:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 10 * time.Second,
	}

	req_path := parsed.RequestURI()
	req, err := client.NewRequest("GET", req_path)
	if err != nil {
		return "", meta, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf")

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", meta, fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", meta, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// PDFs can be large; allow up to 2MB for PDFs, 512KB for HTML.
	content_type := resp.Header.Get("Content-Type")
	is_pdf := strings.Contains(content_type, "application/pdf") || strings.HasSuffix(strings.ToLower(target_url), ".pdf")

	var max_read int64 = 512 * 1024
	if is_pdf {
		max_read = 2 * 1024 * 1024
	}

	limited := io.LimitReader(resp.Body, max_read)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", meta, fmt.Errorf("reading body: %w", err)
	}

	var text string
	if is_pdf {
		text = extractPDFText(body)
	} else {
		html_str := string(body)
		meta = extractMeta(html_str, meta)
		text = extractReadableText(html_str)
	}
	if max_chars > 0 && len(text) > max_chars {
		// Truncate at a word boundary.
		text = text[:max_chars]
		if idx := strings.LastIndexByte(text, ' '); idx > max_chars/2 {
			text = text[:idx]
		}
		text += "..."
	}
	return text, meta, nil
}

// extractMetaContent finds a <meta> tag by name or property and returns its content.
func extractMetaContent(html string, attr string, value string) string {
	lower := strings.ToLower(html)
	target := strings.ToLower(attr + "=\"" + value + "\"")
	idx := strings.Index(lower, target)
	if idx < 0 {
		return ""
	}
	// Find the enclosing <meta ...> tag.
	start := strings.LastIndex(lower[:idx], "<meta")
	if start < 0 {
		return ""
	}
	end := strings.Index(lower[start:], ">")
	if end < 0 {
		return ""
	}
	tag := html[start : start+end+1]
	// Extract content="..."
	ci := strings.Index(strings.ToLower(tag), "content=\"")
	if ci < 0 {
		return ""
	}
	rest := tag[ci+9:]
	if qi := strings.Index(rest, "\""); qi >= 0 {
		return strings.TrimSpace(rest[:qi])
	}
	return ""
}

// extractMeta parses HTML meta tags for article metadata.
func extractMeta(html string, meta SourceMeta) SourceMeta {
	// Title: og:title > <title>
	if t := extractMetaContent(html, "property", "og:title"); t != "" {
		meta.Title = t
	} else {
		lower := strings.ToLower(html)
		if idx := strings.Index(lower, "<title"); idx >= 0 {
			rest := html[idx:]
			if gt := strings.Index(rest, ">"); gt >= 0 {
				rest = rest[gt+1:]
				if end := strings.Index(strings.ToLower(rest), "</title>"); end >= 0 {
					meta.Title = strings.TrimSpace(stripTags(rest[:end]))
				}
			}
		}
	}

	// Author
	if a := extractMetaContent(html, "name", "author"); a != "" {
		meta.Author = a
	} else if a := extractMetaContent(html, "property", "article:author"); a != "" {
		meta.Author = a
	}

	// Site name
	if s := extractMetaContent(html, "property", "og:site_name"); s != "" {
		meta.Site = s
	}

	// Publication date: article:published_time > datePublished > date
	if d := extractMetaContent(html, "property", "article:published_time"); d != "" {
		meta.PubDate = d
	} else if d := extractMetaContent(html, "name", "date"); d != "" {
		meta.PubDate = d
	} else if d := extractMetaContent(html, "name", "pubdate"); d != "" {
		meta.PubDate = d
	} else if d := extractMetaContent(html, "property", "datePublished"); d != "" {
		meta.PubDate = d
	}

	return meta
}

// DomainCredibility returns a score (0-100) estimating source reliability
// based on the domain. Higher is more credible.
func DomainCredibility(domain string) int {
	domain = strings.ToLower(domain)

	// Strip www. prefix for matching.
	domain = strings.TrimPrefix(domain, "www.")

	// Exact domain matches for high-credibility sources.
	high_credibility := map[string]int{
		// Fact-check organizations
		"snopes.com":         90,
		"politifact.com":     90,
		"factcheck.org":      90,
		"fullfact.org":       85,
		"apnews.com":         85,
		"reuters.com":        85,
		// Major scientific publishers
		"nature.com":         95,
		"science.org":        95,
		"thelancet.com":      95,
		"nejm.org":           95,
		"pubmed.ncbi.nlm.nih.gov": 95,
		"scholar.google.com": 80,
		// Reference
		"wikipedia.org":      70,
		"en.wikipedia.org":   70,
		"britannica.com":     80,
		// Major news
		"bbc.com":            80,
		"bbc.co.uk":          80,
		"nytimes.com":        80,
		"washingtonpost.com": 80,
		"theguardian.com":    78,
	}

	if score, ok := high_credibility[domain]; ok {
		return score
	}

	// TLD-based scoring.
	if strings.HasSuffix(domain, ".gov") || strings.HasSuffix(domain, ".gov.uk") {
		return 90
	}
	if strings.HasSuffix(domain, ".edu") || strings.HasSuffix(domain, ".ac.uk") {
		return 85
	}
	if strings.HasSuffix(domain, ".org") {
		return 60
	}

	// Check for subdomains of credible domains.
	for d, score := range high_credibility {
		if strings.HasSuffix(domain, "."+d) {
			return score
		}
	}

	// Default score for unknown domains.
	return 40
}

// CredibilityLabel returns a human-readable credibility rating for the given score.
func CredibilityLabel(score int) string {
	if score >= 85 {
		return "High"
	} else if score >= 60 {
		return "Medium"
	} else if score >= 40 {
		return "Low"
	}
	return "Unknown"
}

// FormatArticleMeta builds a bracketed metadata line from a SourceMeta, e.g.
// "[Author: Jane Doe | Published: 2024-03-01 | Source: NYT | Credibility: High]"
func FormatArticleMeta(meta SourceMeta) string {
	var parts []string
	if meta.Author != "" {
		parts = append(parts, "Author: "+meta.Author)
	}
	if meta.PubDate != "" {
		parts = append(parts, "Published: "+meta.PubDate)
	}
	if meta.Site != "" {
		parts = append(parts, "Source: "+meta.Site)
	}
	parts = append(parts, "Credibility: "+CredibilityLabel(DomainCredibility(meta.Domain)))
	return "[" + strings.Join(parts, " | ") + "]"
}

// IsBlockedURL returns true for sites that don't yield useful article content
// (video platforms, social media, etc.).
func IsBlockedURL(u string) bool {
	lower := strings.ToLower(u)
	blocked := []string{
		"youtube.com", "youtu.be", "vimeo.com", "dailymotion.com",
		"tiktok.com", "twitch.tv", "rumble.com", "bitchute.com",
		"instagram.com", "facebook.com", "twitter.com", "x.com",
		"threads.net", "reddit.com", "linkedin.com", "pinterest.com", "quora.com",
	}
	for _, d := range blocked {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}

// DomainCategories holds curated authoritative domain lists by topic category.
// Used by DiscoverDomains to provide topic-specific search guidance.
var DomainCategories = map[string]string{
	"legal": "- site:scholar.google.com (case law search)\n- site:courtlistener.com (federal/state court opinions)\n- site:law.justia.com (case law and codes)\n- site:law.cornell.edu (Legal Information Institute)\n- site:supremecourt.gov (Supreme Court opinions)\n- site:casetext.com (case law research)\n- site:congress.gov (legislation and CRS reports)",
	"medical": "- site:pubmed.ncbi.nlm.nih.gov (medical research)\n- site:pmc.ncbi.nlm.nih.gov (full-text medical articles)\n- site:who.int (World Health Organization)\n- site:cdc.gov (CDC data and guidelines)\n- site:cochranelibrary.com (systematic reviews)\n- site:fda.gov (drug/device regulations)",
	"economic": "- site:bls.gov (labor statistics)\n- site:census.gov (demographic and economic data)\n- site:nber.org (economic research papers)\n- site:cbo.gov (Congressional Budget Office)\n- site:imf.org (international economic data)\n- site:worldbank.org (global economic data)\n- site:fred.stlouisfed.org (Federal Reserve data)",
	"scientific": "- site:nature.com (Nature journal)\n- site:science.org (Science journal)\n- site:arxiv.org (preprints)\n- site:pnas.org (Proceedings of the National Academy)\n- site:nih.gov (National Institutes of Health)\n- site:nasa.gov (space/earth science)",
	"political": "- site:congress.gov (legislation)\n- site:gao.gov (Government Accountability Office)\n- site:brookings.edu (policy research)\n- site:rand.org (policy analysis)\n- site:pewresearch.org (public opinion data)",
	"criminal_justice": "- site:bjs.ojp.gov (Bureau of Justice Statistics)\n- site:ussc.gov (US Sentencing Commission)\n- site:sentencingproject.org (sentencing data)\n- site:nij.ojp.gov (National Institute of Justice)\n- site:prisonpolicy.org (incarceration data)\n- site:scholar.google.com (case law search)",
	"environmental": "- site:epa.gov (Environmental Protection Agency)\n- site:ipcc.ch (climate science)\n- site:noaa.gov (atmospheric/oceanic data)\n- site:nature.com (environmental research)\n- site:iea.org (energy data)\n- site:unep.org (UN Environment Programme)",
	"technology": "- site:arxiv.org (CS/AI preprints)\n- site:acm.org (computing research)\n- site:ieee.org (engineering/technology)\n- site:nist.gov (standards and technology)\n- site:ftc.gov (tech regulation)\n- site:eff.org (digital rights analysis)",
	"education": "- site:ed.gov (Department of Education)\n- site:nces.ed.gov (education statistics)\n- site:oecd.org (international education data)\n- site:nber.org (education economics)\n- site:rand.org (education policy)",
	"military": "- site:defense.gov (Department of Defense)\n- site:sipri.org (arms/military spending data)\n- site:rand.org (defense analysis)\n- site:cbo.gov (defense budget analysis)\n- site:iiss.org (strategic studies)",
}

// DiscoverDomains classifies a topic into research categories and returns
// curated authoritative domain recommendations. The classify function is called
// to ask an LLM to pick 1-3 categories from the available set.
func DiscoverDomains(topic, posFor, posAgainst string, classify func(prompt string) (string, error)) (string, []string) {
	var categories []string
	for k := range DomainCategories {
		categories = append(categories, k)
	}

	prompt := fmt.Sprintf(`Classify this debate topic into 1-3 research categories.

Topic: "%s"
FOR: "%s"
AGAINST: "%s"

Available categories: %s

Strict guidance:
- "legal" ONLY for case law / specific court rulings / litigation. NOT for policy or laws being passed.
- "criminal_justice" ONLY for crimes, sentencing, policing, incarceration
- "military" for warfare, defense, weapons, international security
- "political" for laws, policy, treaties, regulations, government action
- "technology" only for civilian tech — military tech goes under "military"

Reply with ONLY a JSON array of 1-3 category names that best fit this topic:
["category1", "category2"]`, topic, posFor, posAgainst, strings.Join(categories, ", "))

	text, err := classify(prompt)
	if err != nil {
		return "", nil
	}

	text = strings.TrimSpace(text)
	if start := strings.Index(text, "["); start >= 0 {
		if end := strings.LastIndex(text, "]"); end > start {
			text = text[start : end+1]
		}
	}
	var picked []string
	if json.Unmarshal([]byte(text), &picked) != nil || len(picked) == 0 {
		return "", nil
	}

	var sb strings.Builder
	var validCategories []string
	seen := make(map[string]bool)
	for _, cat := range picked {
		cat = strings.ToLower(strings.TrimSpace(cat))
		if domains, ok := DomainCategories[cat]; ok {
			validCategories = append(validCategories, cat)
			for _, line := range strings.Split(domains, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !seen[line] {
					seen[line] = true
					fmt.Fprintln(&sb, line)
				}
			}
		}
	}
	return sb.String(), validCategories
}

// extractReadableText strips HTML and extracts the main text content.
// It removes script, style, nav, header, and footer elements, then
// collapses whitespace for a clean readable output.
func extractReadableText(html string) string {
	content := html

	// Remove elements that don't contain article content.
	for _, tag := range []string{"script", "style", "nav", "header", "footer", "noscript", "svg", "iframe"} {
		for {
			open := strings.Index(strings.ToLower(content), "<"+tag)
			if open < 0 || open >= len(content) {
				break
			}
			close_tag := "</" + tag + ">"
			end := strings.Index(strings.ToLower(content[open:]), close_tag)
			if end < 0 {
				if gt := strings.Index(content[open:], ">"); gt >= 0 {
					content = content[:open] + content[open+gt+1:]
				} else {
					break
				}
			} else {
				cut := open + end + len(close_tag)
				if cut > len(content) {
					cut = len(content)
				}
				content = content[:open] + content[cut:]
			}
		}
	}

	// Try to find main content area.
	best := content
	for _, marker := range []string{"<article", "<main", "id=\"content\"", "id=\"article\"", "class=\"article"} {
		if idx := strings.Index(strings.ToLower(best), marker); idx >= 0 {
			candidate := best[idx:]
			if marker == "<article" || marker == "<main" {
				tag_name := marker[1:]
				close_tag := "</" + tag_name + ">"
				if end := strings.Index(strings.ToLower(candidate), close_tag); end >= 0 {
					candidate = candidate[:end]
				}
			}
			if len(stripTags(candidate)) > 200 {
				best = candidate
				break
			}
		}
	}

	text := stripTags(best)

	// Collapse whitespace, preserving paragraph breaks.
	var out strings.Builder
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := collapseSpaces(strings.TrimSpace(line))
		if trimmed == "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			continue
		}
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n\n") {
			out.WriteString(" ")
		}
		out.WriteString(trimmed)
	}

	return strings.TrimSpace(out.String())
}

// collapseSpaces replaces runs of whitespace with a single space.
func collapseSpaces(s string) string {
	var out strings.Builder
	was_space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !was_space {
				out.WriteRune(' ')
				was_space = true
			}
		} else {
			out.WriteRune(r)
			was_space = false
		}
	}
	return out.String()
}

// extractPDFText extracts readable text from a PDF byte slice.
// Handles FlateDecode compressed streams and common text operators.
func extractPDFText(data []byte) string {
	var all_text strings.Builder

	// Find and decompress all streams in the PDF.
	for i := 0; i < len(data); {
		// Find next stream.
		idx := bytes.Index(data[i:], []byte("stream"))
		if idx < 0 {
			break
		}
		stream_start := i + idx + len("stream")
		// Skip \r\n or \n after "stream".
		if stream_start < len(data) && data[stream_start] == '\r' {
			stream_start++
		}
		if stream_start < len(data) && data[stream_start] == '\n' {
			stream_start++
		}

		// Find endstream.
		end_idx := bytes.Index(data[stream_start:], []byte("endstream"))
		if end_idx < 0 {
			break
		}
		stream_data := data[stream_start : stream_start+end_idx]

		// Check if this stream's dictionary has FlateDecode.
		// Look backwards from "stream" for the dictionary.
		dict_region := data[i : i+idx]
		is_flate := bytes.Contains(dict_region, []byte("FlateDecode"))

		var decoded []byte
		if is_flate {
			r, err := zlib.NewReader(bytes.NewReader(stream_data))
			if err == nil {
				decoded, _ = io.ReadAll(io.LimitReader(r, 256*1024))
				r.Close()
			}
		} else {
			decoded = stream_data
		}

		if len(decoded) > 0 {
			text := extractTextFromPDFStream(string(decoded))
			if text != "" {
				all_text.WriteString(text)
				all_text.WriteString("\n")
			}
		}

		i = stream_start + end_idx + len("endstream")
	}

	result := strings.TrimSpace(all_text.String())

	// Clean up: collapse excessive whitespace while preserving paragraphs.
	var cleaned strings.Builder
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		trimmed := collapseSpaces(strings.TrimSpace(line))
		if trimmed == "" {
			if cleaned.Len() > 0 {
				cleaned.WriteString("\n\n")
			}
			continue
		}
		if cleaned.Len() > 0 && !strings.HasSuffix(cleaned.String(), "\n\n") {
			cleaned.WriteString(" ")
		}
		cleaned.WriteString(trimmed)
	}

	return strings.TrimSpace(cleaned.String())
}

// Regex patterns for PDF text operators.
var (
	pdfTjPattern  = regexp.MustCompile(`\(([^)]*)\)\s*Tj`)
	pdfTJPattern  = regexp.MustCompile(`\[([^\]]*)\]\s*TJ`)
	pdfTJStrings  = regexp.MustCompile(`\(([^)]*)\)`)
	pdfQuotePattern = regexp.MustCompile(`\(([^)]*)\)\s*['"]\s`)
)

// extractTextFromPDFStream extracts text from a decompressed PDF content stream
// by parsing text-showing operators: Tj, TJ, ', ".
func extractTextFromPDFStream(stream string) string {
	var out strings.Builder

	// Handle Tj operator: (text) Tj
	for _, match := range pdfTjPattern.FindAllStringSubmatch(stream, -1) {
		out.WriteString(decodePDFString(match[1]))
	}

	// Handle TJ operator: [(text) num (text) ...] TJ
	for _, match := range pdfTJPattern.FindAllStringSubmatch(stream, -1) {
		array := match[1]
		for _, s := range pdfTJStrings.FindAllStringSubmatch(array, -1) {
			out.WriteString(decodePDFString(s[1]))
		}
	}

	// Handle ' and " operators: (text) '
	for _, match := range pdfQuotePattern.FindAllStringSubmatch(stream, -1) {
		out.WriteString(decodePDFString(match[1]))
	}

	// Also look for BT...ET blocks with plain text that might not match operators.
	// Some PDFs use simple text placement without standard operators.

	return out.String()
}

// decodePDFString handles basic PDF string escape sequences.
func decodePDFString(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				out.WriteRune('\n')
			case 'r':
				out.WriteRune('\r')
			case 't':
				out.WriteRune('\t')
			case '(':
				out.WriteRune('(')
			case ')':
				out.WriteRune(')')
			case '\\':
				out.WriteRune('\\')
			default:
				// Octal escape or unknown — skip.
				if s[i+1] >= '0' && s[i+1] <= '7' {
					val := int(s[i+1] - '0')
					j := i + 2
					for k := 0; k < 2 && j < len(s) && s[j] >= '0' && s[j] <= '7'; k++ {
						val = val*8 + int(s[j]-'0')
						j++
					}
					if val > 0 && val < 128 {
						out.WriteByte(byte(val))
					}
					i = j
					continue
				}
				out.WriteByte(s[i+1])
			}
			i += 2
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}
