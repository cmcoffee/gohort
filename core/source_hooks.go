package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

const sourceHookTable = "source_hooks"

// SourceHookType identifies the kind of integration.
type SourceHookType string

const (
	HookTypeAPI     SourceHookType = "api"     // REST API endpoint returning search results
	HookTypeRAG     SourceHookType = "rag"     // RAG endpoint returning document chunks
	HookTypePaywall SourceHookType = "paywall" // Paywall bypass — adds auth headers when fetching URLs
)

// SourceHookAuth identifies the authentication method.
type SourceHookAuth string

const (
	HookAuthNone   SourceHookAuth = "none"
	HookAuthAPIKey SourceHookAuth = "api_key" // API key in header or query param
	HookAuthBearer SourceHookAuth = "bearer"  // Bearer token in Authorization header
)

// SourceHook is a configured external source integration.
type SourceHook struct {
	Name     string         `json:"name"`      // Display name (e.g., "Westlaw", "PubMed API")
	Type     SourceHookType `json:"type"`      // api, rag, paywall
	Endpoint string         `json:"endpoint"`  // Base URL for API/RAG, or empty for paywall
	AuthType SourceHookAuth `json:"auth_type"` // none, api_key, bearer
	AuthKey  string         `json:"auth_key"`  // API key or bearer token (stored encrypted)

	// API/RAG hooks: how to call the endpoint.
	QueryParam   string `json:"query_param"`   // Query parameter name (e.g., "q", "query", "term")
	ResultsPath  string `json:"results_path"`  // JSON path to results array (e.g., "results", "data.items")
	TitleField   string `json:"title_field"`   // Field name for result title (default: "title")
	URLField     string `json:"url_field"`     // Field name for result URL (default: "url")
	SnippetField string `json:"snippet_field"` // Field name for snippet (default: "snippet")
	ContentField string `json:"content_field"` // Field name for full content (RAG) (default: "content")

	// Paywall hooks: which domains to apply auth to.
	Domains []string `json:"domains"` // Domains this hook applies to (e.g., ["wsj.com", "ft.com"])

	// Trigger: when to use this hook.
	TriggerDomains []string `json:"trigger_domains"` // Topic domains that activate this hook (e.g., ["legal", "medical"])
	AlwaysActive   bool     `json:"always_active"`   // Always search this hook, regardless of topic

	// Rate limiting.
	MaxRPS int `json:"max_rps"` // Max requests per second (0 = unlimited)
}

// sourceHookRegistry manages all configured hooks.
var sourceHookRegistry struct {
	mu    sync.RWMutex
	hooks []SourceHook
}

// RegisteredSourceHooks returns all configured source hooks.
func RegisteredSourceHooks() []SourceHook {
	sourceHookRegistry.mu.RLock()
	defer sourceHookRegistry.mu.RUnlock()
	cp := make([]SourceHook, len(sourceHookRegistry.hooks))
	copy(cp, sourceHookRegistry.hooks)
	return cp
}

// LoadSourceHooks reads hooks from the database.
func LoadSourceHooks(db Database) {
	if db == nil {
		return
	}
	sourceHookRegistry.mu.Lock()
	defer sourceHookRegistry.mu.Unlock()
	sourceHookRegistry.hooks = nil
	for _, key := range db.Keys(sourceHookTable) {
		if strings.HasSuffix(key, "_auth") {
			continue // Skip encrypted auth keys — they're loaded separately.
		}
		var hook SourceHook
		if db.Get(sourceHookTable, key, &hook) {
			// Decrypt the auth key.
			var authKey string
			if db.Get(sourceHookTable, key+"_auth", &authKey) {
				hook.AuthKey = authKey
			}
			sourceHookRegistry.hooks = append(sourceHookRegistry.hooks, hook)
		}
	}
	keys := db.Keys(sourceHookTable)
	Log("[hooks] source_hooks table has %d keys, loaded %d hooks", len(keys), len(sourceHookRegistry.hooks))
}

// SaveSourceHook stores a hook in the database.
func SaveSourceHook(db Database, hook SourceHook) {
	if db == nil {
		return
	}
	key := strings.ToLower(strings.ReplaceAll(hook.Name, " ", "_"))
	// Store auth key encrypted, separately.
	authKey := hook.AuthKey
	hook.AuthKey = "" // Don't store in plaintext.
	db.Set(sourceHookTable, key, hook)
	if authKey != "" {
		db.CryptSet(sourceHookTable, key+"_auth", authKey)
	}
	// Reload registry.
	LoadSourceHooks(db)
}

// DeleteSourceHook removes a hook from the database.
func DeleteSourceHook(db Database, name string) {
	if db == nil {
		return
	}
	key := strings.ToLower(strings.ReplaceAll(name, " ", "_"))
	db.Unset(sourceHookTable, key)
	db.Unset(sourceHookTable, key+"_auth")
	LoadSourceHooks(db)
}

// HooksForTopic returns hooks that should be queried for the given topic domains.
func HooksForTopic(topic_domains []string) []SourceHook {
	sourceHookRegistry.mu.RLock()
	defer sourceHookRegistry.mu.RUnlock()

	domain_set := make(map[string]bool)
	for _, d := range topic_domains {
		domain_set[strings.ToLower(d)] = true
	}

	var matched []SourceHook
	for _, hook := range sourceHookRegistry.hooks {
		if hook.AlwaysActive {
			matched = append(matched, hook)
			continue
		}
		for _, td := range hook.TriggerDomains {
			if domain_set[strings.ToLower(td)] {
				matched = append(matched, hook)
				break
			}
		}
	}
	return matched
}

// HookForDomain returns a paywall hook matching the given URL domain, or nil.
func HookForDomain(target_url string) *SourceHook {
	parsed, err := url.Parse(target_url)
	if err != nil {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())

	sourceHookRegistry.mu.RLock()
	defer sourceHookRegistry.mu.RUnlock()

	for i, hook := range sourceHookRegistry.hooks {
		if hook.Type != HookTypePaywall {
			continue
		}
		for _, d := range hook.Domains {
			if strings.HasSuffix(host, strings.ToLower(d)) {
				return &sourceHookRegistry.hooks[i]
			}
		}
	}
	return nil
}

// queryPubMed performs the two-step esearch→esummary PubMed lookup.
func queryPubMed(hook SourceHook, query string) (string, error) {
	client := &apiclient.APIClient{
		Server:         "eutils.ncbi.nlm.nih.gov",
		URLScheme:      "https",
		VerifySSL:      true,
		RequestTimeout: 15 * time.Second,
	}

	// Step 1: esearch to get PMIDs.
	search_path := fmt.Sprintf("/entrez/eutils/esearch.fcgi?db=pubmed&retmode=json&retmax=5&sort=relevance&term=%s", url.QueryEscape(query))
	if hook.AuthKey != "" {
		search_path += "&api_key=" + url.QueryEscape(hook.AuthKey)
	}
	req, err := client.NewRequest("GET", search_path)
	if err != nil {
		return "", fmt.Errorf("PubMed esearch request: %w", err)
	}
	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("PubMed esearch: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var searchResult struct {
		ESearchResult struct {
			IDList []string `json:"idlist"`
		} `json:"esearchresult"`
	}
	if err := json.Unmarshal(body, &searchResult); err != nil || len(searchResult.ESearchResult.IDList) == 0 {
		return "", nil
	}

	// Step 2: esummary to get article details.
	ids := strings.Join(searchResult.ESearchResult.IDList, ",")
	summary_path := fmt.Sprintf("/entrez/eutils/esummary.fcgi?db=pubmed&retmode=json&id=%s", ids)
	if hook.AuthKey != "" {
		summary_path += "&api_key=" + url.QueryEscape(hook.AuthKey)
	}
	req2, err := client.NewRequest("GET", summary_path)
	if err != nil {
		return "", fmt.Errorf("PubMed esummary request: %w", err)
	}
	resp2, err := client.SendRawRequest("", req2)
	if err != nil {
		return "", fmt.Errorf("PubMed esummary: %w", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)

	var summaryResult struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body2, &summaryResult); err != nil {
		return "", fmt.Errorf("PubMed parse: %w", err)
	}

	var sb strings.Builder
	n := 0
	for uid, raw := range summaryResult.Result {
		if uid == "uids" {
			continue
		}
		var item struct {
			Title  string `json:"title"`
			Source string `json:"source"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Title == "" {
			continue
		}
		n++
		fmt.Fprintf(&sb, "%d. %s\n", n, item.Title)
		fmt.Fprintf(&sb, "   https://pubmed.ncbi.nlm.nih.gov/%s/\n", uid)
		if item.Source != "" {
			fmt.Fprintf(&sb, "   Published in: %s\n", item.Source)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// sanitizeEDGARQuery strips field-tag syntax that SEC EDGAR's full-text
// search API doesn't parse. LLM-generated queries often look like:
//
//	company:"Baidu" AND (form type:"10-K" OR form type:"20-F") AND (keyword:"AI" OR keyword:"machine learning")
//
// EDGAR's actual query syntax is simple Lucene:
//
//	"Baidu" ("10-K" OR "20-F") ("AI" OR "machine learning")
//
// Every `field:value` construct produces zero results because EDGAR
// interprets the colon literally. This function:
//   - Strips field tags like "company:", "form type:", "keyword:",
//     "ticker:", "cik:", leaving the quoted/bare value.
//   - Strips "form type" entirely — form filtering happens via the
//     forms= URL parameter, not the q string.
//   - Keeps boolean operators (AND/OR/NOT), parentheses, and phrase
//     quotes, which EDGAR does support.
//   - Normalizes whitespace.
//
// Result: a query shape EDGAR can actually process. Companies that
// were previously "Baidu" (cached as empty) now resolve to filings
// because the sanitized query is "Baidu" AND "10-K" AND "AI"… which
// EDGAR treats as a real keyword search.
func sanitizeEDGARQuery(query string) string {
	q := query
	// Strip common field-tag prefixes. Order matters — multi-word tags
	// first so "form type:" is caught before a bare "type:" rule could
	// hit it. All match `tag:` including surrounding whitespace so the
	// value survives intact.
	fieldTags := []string{
		"company:", "form type:", "formtype:", "form:",
		"keyword:", "keywords:",
		"ticker:", "cik:", "sic:",
		"name:", "filer:", "issuer:",
		"title:", "abstract:", "content:",
	}
	// Case-insensitive strip.
	lower := strings.ToLower(q)
	for _, tag := range fieldTags {
		for {
			idx := strings.Index(lower, tag)
			if idx < 0 {
				break
			}
			q = q[:idx] + q[idx+len(tag):]
			lower = strings.ToLower(q)
		}
	}
	// Collapse whitespace.
	q = strings.Join(strings.Fields(q), " ")
	// Trim trailing/leading stray parens if they became unbalanced.
	for strings.HasPrefix(q, ")") || strings.HasPrefix(q, "AND") || strings.HasPrefix(q, "OR") {
		if strings.HasPrefix(q, ")") {
			q = strings.TrimSpace(q[1:])
		} else if strings.HasPrefix(q, "AND") {
			q = strings.TrimSpace(q[3:])
		} else if strings.HasPrefix(q, "OR") {
			q = strings.TrimSpace(q[2:])
		}
	}
	for strings.HasSuffix(q, "(") || strings.HasSuffix(q, "AND") || strings.HasSuffix(q, "OR") {
		if strings.HasSuffix(q, "(") {
			q = strings.TrimSpace(q[:len(q)-1])
		} else if strings.HasSuffix(q, "AND") {
			q = strings.TrimSpace(q[:len(q)-3])
		} else if strings.HasSuffix(q, "OR") {
			q = strings.TrimSpace(q[:len(q)-2])
		}
	}
	return q
}

// queryEDGAR searches the SEC EDGAR full-text search API for filings.
// queryOpenAlex searches the OpenAlex API for academic papers.
// OpenAlex returns abstracts as inverted indexes (word -> positions map)
// which must be reconstructed into plain text.
func queryOpenAlex(hook SourceHook, query string) (string, error) {
	// Strip site: filters and quotes for API search.
	if idx := strings.Index(query, "site:"); idx >= 0 {
		before := query[:idx]
		after := ""
		rest := query[idx+5:]
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			after = rest[sp:]
		}
		query = strings.TrimSpace(before + after)
	}
	query = strings.NewReplacer("\"", "", "'", "").Replace(query)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}

	endpoint := hook.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openalex.org/works?per_page=10"
	}
	// OpenAlex requests a mailto parameter for polite pool access.
	mailCfg := LoadMailConfig()
	if mailCfg.From != "" && !strings.Contains(endpoint, "mailto=") {
		endpoint += "&mailto=" + url.QueryEscape(mailCfg.From)
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parsing OpenAlex endpoint: %w", err)
	}

	client := &apiclient.APIClient{
		Server:         parsed.Host,
		URLScheme:      parsed.Scheme,
		VerifySSL:      true,
		RequestTimeout: 15 * time.Second,
	}

	req_path := parsed.Path
	if parsed.RawQuery != "" {
		req_path += "?" + parsed.RawQuery + "&search=" + url.QueryEscape(query)
	} else {
		req_path += "?search=" + url.QueryEscape(query)
	}

	req, err := client.NewRequest("GET", req_path)
	if err != nil {
		return "", fmt.Errorf("creating OpenAlex request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("querying OpenAlex: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAlex returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading OpenAlex response: %w", err)
	}

	var data struct {
		Results []struct {
			Title              string                     `json:"title"`
			DOI                string                     `json:"doi"`
			PublicationYear    int                        `json:"publication_year"`
			CitedByCount       int                        `json:"cited_by_count"`
			AbstractInvertedIndex map[string][]int         `json:"abstract_inverted_index"`
			OpenAccess         struct {
				OAURL string `json:"oa_url"`
			} `json:"open_access"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("parsing OpenAlex response: %w", err)
	}

	var sb strings.Builder
	for i, work := range data.Results {
		if work.Title == "" {
			continue
		}

		// Prefer open access URL, fall back to DOI.
		link := work.OpenAccess.OAURL
		if link == "" {
			link = work.DOI
		}
		if link == "" {
			continue
		}

		// Reconstruct abstract from inverted index.
		var abstract string
		if len(work.AbstractInvertedIndex) > 0 {
			words := make(map[int]string)
			maxPos := 0
			for word, positions := range work.AbstractInvertedIndex {
				for _, pos := range positions {
					words[pos] = word
					if pos > maxPos {
						maxPos = pos
					}
				}
			}
			parts := make([]string, 0, maxPos+1)
			for j := 0; j <= maxPos; j++ {
				if w, ok := words[j]; ok {
					parts = append(parts, w)
				}
			}
			abstract = strings.Join(parts, " ")
			if len(abstract) > 300 {
				abstract = abstract[:300] + "..."
			}
		}

		fmt.Fprintf(&sb, "%d. %s", i+1, work.Title)
		if work.PublicationYear > 0 {
			fmt.Fprintf(&sb, " (%d)", work.PublicationYear)
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "   %s\n", link)
		if abstract != "" {
			fmt.Fprintf(&sb, "   %s\n", abstract)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

func queryEDGAR(hook SourceHook, query string) (string, error) {
	// Sanitize the query first — LLM-generated EDGAR queries often use
	// field-tag syntax (company:"X", form type:"10-K", keyword:"AI")
	// that the EDGAR API doesn't parse, causing every query to return
	// empty. sanitizeEDGARQuery strips the scaffolding and leaves a
	// keyword-style query EDGAR can handle.
	sanitized := sanitizeEDGARQuery(query)
	if sanitized != query {
		Debug("[source-hooks] EDGAR sanitized query: %q -> %q", query, sanitized)
	}
	if sanitized == "" {
		return "", nil
	}

	// SEC EDGAR requires a contact email in the User-Agent per their guidelines.
	contactEmail := "contact@example.com"
	if cfg := LoadMailConfig(); cfg.From != "" {
		contactEmail = cfg.From
	}
	client := &apiclient.APIClient{
		Server:         "efts.sec.gov",
		URLScheme:      "https",
		VerifySSL:      true,
		RequestTimeout: 15 * time.Second,
		AgentString:    fmt.Sprintf("Gohort/%s (%s)", AppVersion, contactEmail),
	}

	search_path := fmt.Sprintf("/LATEST/search-index?q=%s&dateRange=custom&startdt=2024-01-01&enddt=2026-12-31&forms=10-K,10-Q,8-K&from=0&size=10",
		url.QueryEscape(sanitized))

	req, err := client.NewRequest("GET", search_path)
	if err != nil {
		return "", fmt.Errorf("EDGAR request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("EDGAR search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("EDGAR returned HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Hits struct {
			Hits []struct {
				Source struct {
					DisplayNames []string `json:"display_names"`
					CIKs         []string `json:"ciks"`
					Form         string   `json:"form"`
					FileDate     string   `json:"file_date"`
					ADSH         string   `json:"adsh"`
					PeriodEnd    string   `json:"period_ending"`
					FileDesc     string   `json:"file_description"`
				} `json:"_source"`
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("EDGAR parse: %w", err)
	}

	if len(result.Hits.Hits) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for i, hit := range result.Hits.Hits {
		src := hit.Source
		name := "Unknown Filer"
		if len(src.DisplayNames) > 0 {
			// Clean up "INTEL CORP  (INTC)  (CIK 0000050863)" → "Intel Corp (INTC)"
			name = src.DisplayNames[0]
			if idx := strings.Index(name, "(CIK"); idx > 0 {
				name = strings.TrimSpace(name[:idx])
			}
		}

		// Build filing URL from CIK + ADSH + filename.
		cik := "0"
		if len(src.CIKs) > 0 {
			cik = strings.TrimLeft(src.CIKs[0], "0")
			if cik == "" {
				cik = "0"
			}
		}
		adsh_nodash := strings.ReplaceAll(src.ADSH, "-", "")
		filename := ""
		if parts := strings.SplitN(hit.ID, ":", 2); len(parts) == 2 {
			filename = parts[1]
		}
		filing_url := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/%s", cik, adsh_nodash, filename)

		desc := src.Form
		if src.FileDesc != "" && src.FileDesc != src.Form {
			desc += " (" + src.FileDesc + ")"
		}

		fmt.Fprintf(&sb, "%d. %s — %s, filed %s\n", i+1, name, desc, src.FileDate)
		fmt.Fprintf(&sb, "   %s\n", filing_url)
		if src.PeriodEnd != "" {
			fmt.Fprintf(&sb, "   Period ending: %s\n", src.PeriodEnd)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// hookCacheDB is the persistent cache for source hook results. When set,
// QuerySourceHook checks this before making API calls. Keyed by hook name
// + normalized query. Skips the API call entirely on cache hit.
// Main package wires this up via SetHookCacheDB after database init.
var (
	hookCacheDB         Database
	hookCacheMu         sync.RWMutex
	hookCacheTTL        = 30 * 24 * time.Hour // 30 days for non-empty results
	hookCacheEmptyTTL   = 7 * 24 * time.Hour  // 7 days for empty results (negative cache)
	hookCacheBucket     = "source_hook_cache"
)

// hookCacheStopwords are removed from query strings before computing the
// cache key. Stripping filler words like "and", "or", "the", "a" lets
// queries that differ only by common connectives collide in the cache.
// Stopwords from boolean-style CourtListener queries also get stripped.
var hookCacheStopwords = map[string]bool{
	"and": true, "or": true, "not": true,
	"the": true, "a": true, "an": true,
	"of": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "by": true, "with": true,
	"is": true, "are": true, "as": true,
	"how": true, "what": true, "which": true, "who": true,
}

// SetHookCacheDB wires the persistent cache backing store. Call once during
// startup after the database is open. Passing nil disables caching.
func SetHookCacheDB(db Database) {
	hookCacheMu.Lock()
	defer hookCacheMu.Unlock()
	hookCacheDB = db
}

// hookCacheEntry is the stored cache record shape. Empty Result with a
// non-zero FetchedAt is a valid negative-cache entry — it means the live
// API was called and returned nothing, and we're remembering that for
// the shorter empty TTL.
type hookCacheEntry struct {
	Result    string    `json:"result"`
	FetchedAt time.Time `json:"fetched_at"`
}

// hookCacheKey builds the cache key with aggressive query normalization:
//   - lowercase
//   - strip boolean operators and quote marks (CourtListener-style)
//   - tokenize on whitespace
//   - drop stopwords
//   - sort tokens alphabetically
//   - rejoin
//
// This turns "\"moral crumple zone\" AND \"automation\" AND (\"liability\" OR \"responsibility\")"
// and "moral crumple zone automation liability responsibility" and
// "responsibility liability automation moral crumple zone" all into the
// same key "automation crumple liability moral responsibility zone".
// Near-identical LLM-generated queries across runs now collide in the
// cache instead of missing by a single reordered word.
func hookCacheKey(hookName, query string) string {
	q := strings.ToLower(query)
	// Strip quotes and parentheses from boolean-style queries.
	q = strings.NewReplacer(`"`, " ", `(`, " ", `)`, " ", `[`, " ", `]`, " ").Replace(q)
	tokens := strings.Fields(q)
	var keep []string
	seen := make(map[string]bool)
	for _, t := range tokens {
		if hookCacheStopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		keep = append(keep, t)
	}
	sort.Strings(keep)
	return strings.ToLower(strings.TrimSpace(hookName)) + "|" + strings.Join(keep, " ")
}

// lookupHookCache returns a cached result if present and not expired.
// The second return value indicates whether a cache entry was found at
// all — an entry with an empty Result is a valid negative-cache hit
// (the API was called previously and returned nothing). Empty-result
// entries expire on a shorter TTL than non-empty ones.
func lookupHookCache(hookName, query string) (string, bool) {
	hookCacheMu.RLock()
	db := hookCacheDB
	ttl := hookCacheTTL
	emptyTTL := hookCacheEmptyTTL
	hookCacheMu.RUnlock()
	if db == nil {
		return "", false
	}
	key := hookCacheKey(hookName, query)
	var entry hookCacheEntry
	if !db.Get(hookCacheBucket, key, &entry) {
		return "", false
	}
	// Pick the TTL based on whether the stored entry is empty.
	effectiveTTL := ttl
	if entry.Result == "" {
		effectiveTTL = emptyTTL
	}
	if time.Since(entry.FetchedAt) > effectiveTTL {
		return "", false
	}
	return entry.Result, true
}

// storeHookCache writes a query result to the cache. Empty results are
// stored as negative-cache entries with a shorter TTL, so subsequent
// identical queries don't re-hit the live API for known dead-ends.
func storeHookCache(hookName, query, result string) {
	hookCacheMu.RLock()
	db := hookCacheDB
	hookCacheMu.RUnlock()
	if db == nil {
		return
	}
	entry := hookCacheEntry{Result: result, FetchedAt: time.Now()}
	db.Set(hookCacheBucket, hookCacheKey(hookName, query), entry)
}

// authDomainBucket is the kvlite bucket for cached authoritative-domain
// discovery results. Reuses the same hookCacheDB as source hooks, same
// 30-day TTL — no separate setup step needed. Keyed by a hash of the
// normalized research question.
const authDomainBucket = "auth_domain_cache"

// authDomainEntry stores the validated domain list with a fetch timestamp
// for TTL checks.
type authDomainEntry struct {
	Domains   []string  `json:"domains"`
	FetchedAt time.Time `json:"fetched_at"`
}

// LookupAuthDomainCache returns a cached domain list for a research question
// if present and not expired. Returns (nil, false) on miss.
func LookupAuthDomainCache(question string) ([]string, bool) {
	hookCacheMu.RLock()
	db := hookCacheDB
	ttl := hookCacheTTL
	hookCacheMu.RUnlock()
	if db == nil {
		return nil, false
	}
	key := authDomainCacheKey(question)
	var entry authDomainEntry
	if !db.Get(authDomainBucket, key, &entry) {
		return nil, false
	}
	if time.Since(entry.FetchedAt) > ttl {
		return nil, false
	}
	return entry.Domains, true
}

// StoreAuthDomainCache writes a validated domain list for a research question.
// Skips storage when the backing DB is unset or the domain list is empty.
func StoreAuthDomainCache(question string, domains []string) {
	hookCacheMu.RLock()
	db := hookCacheDB
	hookCacheMu.RUnlock()
	if db == nil || len(domains) == 0 {
		return
	}
	entry := authDomainEntry{Domains: domains, FetchedAt: time.Now()}
	db.Set(authDomainBucket, authDomainCacheKey(question), entry)
}

// authDomainCacheKey normalizes the research question for cache lookup.
// Lowercase, trim whitespace, collapse internal spaces — matches the
// hookCacheKey normalization style.
func authDomainCacheKey(question string) string {
	q := strings.ToLower(strings.TrimSpace(question))
	return strings.Join(strings.Fields(q), " ")
}

// QuerySourceHook searches a hook's API/RAG endpoint and returns results
// in the same format as web search results. Wraps the underlying network
// call in a persistent cache (if configured via SetHookCacheDB) so
// identical queries across sessions don't re-hit the upstream API. This
// is critical for rate-limited services like CourtListener and for
// development workflows that repeatedly test the same topic.
func QuerySourceHook(hook SourceHook, query string) (string, error) {
	// Tier 1: exact-key cache lookup. Covers identical normalized
	// queries across runs. An empty cached result is a valid
	// negative-cache hit.
	if cached, ok := lookupHookCache(hook.Name, query); ok {
		if cached == "" {
			Debug("[source-hooks] cache HIT (empty) %s: %q", hook.Name, query)
		} else {
			Debug("[source-hooks] cache HIT %s: %q (%d chars)", hook.Name, query, len(cached))
		}
		return cached, nil
	}

	// Tier 2: document-level search over prior cached results.
	// Handles near-miss queries that share vocabulary with cached
	// content but don't exact-match any prior query key. Only the
	// token buckets for this query's tokens get loaded from disk,
	// so the cost stays bounded regardless of total index size.
	if docs := searchHookDocs(hook.Name, query, 10); len(docs) > 0 {
		result := formatIndexedDocs(docs)
		Debug("[source-hooks] FTS HIT %s: %q (%d docs)", hook.Name, query, len(docs))
		// Store this as a tier-1 cache entry too, so the next
		// identical query gets served from the fast path.
		storeHookCache(hook.Name, query, result)
		return result, nil
	}

	// Tier 3: live API.
	Debug("[source-hooks] cache MISS %s: %q — calling live API", hook.Name, query)
	result, err := queryHookLive(hook, query)
	if err != nil {
		// Errors are NOT cached — they might be transient (rate limit,
		// network issue). Let the next call retry.
		Debug("[source-hooks] cache SKIP-STORE %s: %q — live error: %v", hook.Name, query, err)
		return result, err
	}
	// Store both empty and non-empty results. Empty results use a
	// shorter TTL inside lookupHookCache so they get retried sooner.
	storeHookCache(hook.Name, query, result)
	// Populate the tier-2 document index so future near-miss queries
	// can find this content without hitting the API.
	indexHookDocs(hook.Name, result)
	if result == "" {
		Debug("[source-hooks] cache STORE (negative) %s: %q", hook.Name, query)
	} else {
		Debug("[source-hooks] cache STORE %s: %q (%d chars)", hook.Name, query, len(result))
	}
	return result, err
}

// queryHookLive is the network-backed implementation extracted from the
// previous QuerySourceHook body. Renamed so QuerySourceHook can wrap it
// with cache logic while keeping the concrete fetching isolated.
func queryHookLive(hook SourceHook, query string) (string, error) {
	// Route built-in hooks to custom handlers.
	switch strings.ToLower(hook.Name) {
	case "pubmed":
		return queryPubMed(hook, query)
	case "sec edgar", "edgar", "sec":
		return queryEDGAR(hook, query)
	case "openalex":
		return queryOpenAlex(hook, query)
	}

	if hook.Endpoint == "" {
		return "", fmt.Errorf("hook %q has no endpoint", hook.Name)
	}

	// Strip site: filters — they're for web search engines, not APIs.
	if idx := strings.Index(query, "site:"); idx >= 0 {
		// Remove "site:domain.com" from the query.
		before := query[:idx]
		after := ""
		rest := query[idx+5:]
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			after = rest[sp:]
		}
		query = strings.TrimSpace(before + after)
	}
	if query == "" {
		return "", nil
	}

	// Build request URL.
	param := hook.QueryParam
	if param == "" {
		param = "q"
	}

	// Parse the endpoint URL to extract host, scheme, and path.
	parsed_endpoint, err := url.Parse(hook.Endpoint)
	if err != nil {
		return "", fmt.Errorf("parsing endpoint for %s: %w", hook.Name, err)
	}
	scheme := parsed_endpoint.Scheme
	if scheme == "" {
		scheme = "https"
	}

	// Build the auth function based on hook auth type.
	var auth_func func(req *http.Request)
	switch hook.AuthType {
	case HookAuthAPIKey:
		auth_func = func(req *http.Request) {
			req.Header.Set("X-API-Key", hook.AuthKey)
		}
	case HookAuthBearer:
		auth_func = func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+hook.AuthKey)
		}
	}

	client := &apiclient.APIClient{
		Server:         parsed_endpoint.Host,
		URLScheme:      scheme,
		VerifySSL:      true,
		RequestTimeout: 15 * time.Second,
		AgentString:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		AuthFunc:       auth_func,
	}

	// Build path with query parameters.
	req_path := parsed_endpoint.Path
	if parsed_endpoint.RawQuery != "" {
		req_path += "?" + parsed_endpoint.RawQuery + "&" + url.QueryEscape(param) + "=" + url.QueryEscape(query)
	} else {
		req_path += "?" + url.QueryEscape(param) + "=" + url.QueryEscape(query)
	}

	req, err := client.NewRequest("GET", req_path)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return "", fmt.Errorf("querying %s: %w", hook.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", hook.Name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response from %s: %w", hook.Name, err)
	}

	// Parse JSON response and format as search results.
	return parseHookResults(hook, body)
}

// ApplyPaywallAuth adds authentication headers to an HTTP request
// if a paywall hook matches the target domain.
func ApplyPaywallAuth(req *http.Request) bool {
	hook := HookForDomain(req.URL.String())
	if hook == nil {
		return false
	}
	switch hook.AuthType {
	case HookAuthAPIKey:
		req.Header.Set("X-API-Key", hook.AuthKey)
	case HookAuthBearer:
		req.Header.Set("Authorization", "Bearer "+hook.AuthKey)
	}
	Debug("[hooks] applied %s auth for %s", hook.Name, req.URL.Hostname())
	return true
}

// parseHookResults extracts search results from a JSON API response.
func parseHookResults(hook SourceHook, body []byte) (string, error) {
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		// Not JSON — return as plain text.
		return string(body), nil
	}

	// Navigate to the results array using the results_path.
	results := navigateJSON(raw, hook.ResultsPath)
	if results == nil {
		// Try the root if it's already an array.
		results = raw
	}

	arr, ok := results.([]interface{})
	if !ok {
		// Single result — wrap it.
		arr = []interface{}{results}
	}

	titleField := hook.TitleField
	if titleField == "" {
		titleField = "title"
	}
	urlField := hook.URLField
	if urlField == "" {
		urlField = "url"
	}
	snippetField := hook.SnippetField
	if snippetField == "" {
		snippetField = "snippet"
	}
	contentField := hook.ContentField
	if contentField == "" {
		contentField = "content"
	}

	var sb strings.Builder
	for i, item := range arr {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		title := jsonString(obj, titleField)
		link := jsonString(obj, urlField)
		snippet := jsonString(obj, snippetField)
		content := jsonString(obj, contentField)

		// Fix non-absolute URLs.
		if link != "" && !strings.HasPrefix(link, "http") {
			if strings.HasPrefix(link, "10.") {
				// Bare DOI — prepend https://doi.org/
				link = "https://doi.org/" + link
			} else if strings.HasPrefix(link, "/") {
				// Relative path — prepend endpoint's origin.
				if u, err := url.Parse(hook.Endpoint); err == nil {
					link = u.Scheme + "://" + u.Host + link
				}
			}
		}
		// Fix PubMed IDs — construct URL from pmid field if no link.
		if link == "" {
			if pmid := jsonString(obj, "pmid"); pmid != "" {
				link = "https://pubmed.ncbi.nlm.nih.gov/" + pmid + "/"
			} else if pmcid := jsonString(obj, "pmcid"); pmcid != "" {
				link = "https://pmc.ncbi.nlm.nih.gov/articles/" + pmcid + "/"
			}
		}

		if title == "" && link == "" {
			continue
		}
		if title == "" {
			title = link
		}

		fmt.Fprintf(&sb, "%d. %s\n", i+1, title)
		if link != "" {
			fmt.Fprintf(&sb, "   %s\n", link)
		}
		if snippet != "" {
			// Strip HTML tags from snippets (e.g., CourtListener <mark> tags).
			snippet = stripHTMLTags(snippet)
			fmt.Fprintf(&sb, "   %s\n", snippet)
		} else if content != "" {
			// Use content as snippet, truncated.
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			fmt.Fprintf(&sb, "   %s\n", content)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// navigateJSON follows a dot-separated path through a JSON structure.
func navigateJSON(data interface{}, path string) interface{} {
	if path == "" {
		return data
	}
	parts := strings.Split(path, ".")
	current := data
	for _, part := range parts {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = obj[part]
		if !ok {
			return nil
		}
	}
	return current
}

// stripHTMLTags removes HTML tags from a string.
func stripHTMLTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// jsonString extracts a string value from a JSON object.
func jsonString(obj map[string]interface{}, key string) string {
	val, ok := obj[key]
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", val)
}

// SourceHookTemplate is a pre-configured source hook that users can activate.
type SourceHookTemplate struct {
	Hook        SourceHook
	Description string
	NeedsAPIKey bool
}

// SourceHookTemplates returns pre-configured templates for common sources.
func SourceHookTemplates() []SourceHookTemplate {
	return []SourceHookTemplate{
		{
			Description: "Legal research (case law) — requires Thomson Reuters API access",
			NeedsAPIKey: true,
			Hook: SourceHook{
				Name:           "Westlaw",
				Type:           HookTypeAPI,
				Endpoint:       "https://api.thomsonreuters.com/westlaw/v2/search",
				AuthType:       HookAuthBearer,
				QueryParam:     "query",
				ResultsPath:    "results",
				TitleField:     "title",
				URLField:       "link",
				SnippetField:   "snippet",
				TriggerDomains: []string{"legal", "criminal_justice"},
			},
		},
		{
			Description: "Free case law search — free account at courtlistener.com (case law only, not policy)",
			NeedsAPIKey: true,
			Hook: SourceHook{
				Name:           "CourtListener",
				Type:           HookTypeAPI,
				Endpoint:       "https://www.courtlistener.com/api/rest/v4/search/?type=o&order_by=score+desc",
				AuthType:       HookAuthBearer,
				QueryParam:     "q",
				ResultsPath:    "results",
				TitleField:     "caseName",
				URLField:       "absolute_url",
				SnippetField:   "snippet",
				TriggerDomains: []string{"legal", "criminal_justice"},
			},
		},
		{
			Description: "Academic papers — free API key from semanticscholar.org/product/api",
			NeedsAPIKey: true,
			Hook: SourceHook{
				Name:           "Semantic Scholar",
				Type:           HookTypeAPI,
				Endpoint:       "https://api.semanticscholar.org/graph/v1/paper/search?fields=title,url,abstract",
				AuthType:       HookAuthAPIKey,
				QueryParam:     "query",
				ResultsPath:    "data",
				TitleField:     "title",
				URLField:       "url",
				SnippetField:   "abstract",
				TriggerDomains: []string{"scientific", "medical", "technology", "environmental", "economic", "education"},
			},
		},
		{
			Description: "Biomedical literature — free API key from ncbi.nlm.nih.gov/account",
			NeedsAPIKey: true,
			Hook: SourceHook{
				Name:           "PubMed",
				Type:           HookTypeAPI,
				AuthType:       HookAuthAPIKey,
				TriggerDomains: []string{"medical", "scientific"},
			},
		},
		{
			Description: "Open access academic papers — free API key from CORE",
			NeedsAPIKey: true,
			Hook: SourceHook{
				Name:           "CORE",
				Type:           HookTypeAPI,
				Endpoint:       "https://api.core.ac.uk/v3/search/works",
				AuthType:       HookAuthBearer,
				QueryParam:     "q",
				ResultsPath:    "results",
				TitleField:     "title",
				URLField:       "downloadUrl",
				SnippetField:   "abstract",
				TriggerDomains: []string{"scientific", "medical", "technology", "environmental"},
			},
		},
		{
			Description: "Biomedical and life sciences research — free, no API key",
			NeedsAPIKey: false,
			Hook: SourceHook{
				Name:           "Europe PMC",
				Type:           HookTypeAPI,
				Endpoint:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?format=json&pageSize=5&sort=RELEVANCE",
				AuthType:       HookAuthNone,
				QueryParam:     "query",
				ResultsPath:    "resultList.result",
				TitleField:     "title",
				URLField:       "doi",
				SnippetField:   "abstractText",
				TriggerDomains: []string{"medical", "scientific"},
			},
		},
		{
			Description: "Academic papers across all disciplines — free, no API key",
			NeedsAPIKey: false,
			Hook: SourceHook{
				Name:           "OpenAlex",
				Type:           HookTypeAPI,
				Endpoint:       "https://api.openalex.org/works?per_page=10",
				AuthType:       HookAuthNone,
				QueryParam:     "search",
				ResultsPath:    "results",
				TitleField:     "title",
				URLField:       "doi",
				SnippetField:   "",
				TriggerDomains: []string{},
			},
		},
		{
			Description: "SEC filings, earnings calls, 10-K/10-Q/8-K — free, no API key",
			NeedsAPIKey: false,
			Hook: SourceHook{
				Name:           "SEC EDGAR",
				Type:           HookTypeAPI,
				AuthType:       HookAuthNone,
				TriggerDomains: []string{"financial", "economic", "corporate", "investment"},
			},
		},
	}
}
