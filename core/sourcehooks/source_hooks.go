package sourcehooks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

var (
	HTTPConnectTimeout = 10 * time.Second
	HTTPRequestTimeout = 15 * time.Second
)

// ApplyHTTPTimeouts reads connect/request timeout values from the database and
// updates the package-level vars used by all source-hook API clients. Called
// once at startup and again after --setup saves new values.
func ApplyHTTPTimeouts(db Store) {
	var connect_sec, request_sec int
	db.Get(NetworkConfigTable, "connect_timeout_seconds", &connect_sec)
	db.Get(NetworkConfigTable, "request_timeout_seconds", &request_sec)
	if connect_sec > 0 {
		HTTPConnectTimeout = time.Duration(connect_sec) * time.Second
	}
	if request_sec > 0 {
		HTTPRequestTimeout = time.Duration(request_sec) * time.Second
	}
}

// NewBoundedHTTPClient builds an HTTP client whose CONNECTION-PHASE
// timeouts come from the operator-configured Network Timeouts
// (HTTPConnectTimeout / HTTPRequestTimeout, loaded by ApplyHTTPTimeouts):
//
//	dial + TLS handshake  → HTTPConnectTimeout (default 10s)
//	time-to-first-byte    → HTTPRequestTimeout (default 15s) via ResponseHeaderTimeout
//
// It sets NO client-level Timeout — the OVERALL bound stays the caller's
// request context (context.WithTimeout on the *http.Request), so a slow-
// but-progressing large body download still gets its time while a dead
// server (accepts the TCP connection but never sends response headers —
// the classic hang) fails at the configured request timeout instead of
// stalling for the full context window.
//
// Build PER-CALL (it's cheap — a cloned transport) so it reflects --setup
// changes that mutate the timeout vars at runtime. Use for fetching
// arbitrary public URLs (fetch_url, collection autofill) where a dead
// host must not hang the caller — an agent round, a pipeline stage, an
// autofill batch. Internal/bounded API calls that already carry an
// apiclient timeout don't need this.
func NewBoundedHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: HTTPConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = HTTPConnectTimeout
	tr.ResponseHeaderTimeout = HTTPRequestTimeout
	return &http.Client{Transport: tr}
}

// NewPublicHTTPClient is NewBoundedHTTPClient for a URL that must be on the
// public internet (fetch_url, a script's fetch, collection autofill, image
// and monitor fetches). The host check the callers already make reads the URL
// as written, which a hostname resolving to 127.0.0.1 or 169.254.169.254, a
// DNS answer that changes between check and dial, or a public page that
// redirects inward all walk past. This checks the address actually DIALLED,
// after resolution, on every connection including each redirect's, and
// re-checks a redirect's scheme.
//
// Not for credential-routed calls: a credential names its own (possibly
// internal) host on purpose, and those go through the credential's client.
func NewPublicHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // a proxy would dial on our behalf, past the check below
	tr.DialContext = PublicDialer().DialContext
	tr.TLSHandshakeTimeout = HTTPConnectTimeout
	tr.ResponseHeaderTimeout = HTTPRequestTimeout
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect to a %s: URL", req.URL.Scheme)
			}
			return nil
		},
	}
}

// PublicDialer dials public addresses only, checked on the address actually
// dialled, after resolution: the dialer under NewPublicHTTPClient, for a
// caller that needs a connection rather than a request (the sandbox relay's
// tunnels).
func PublicDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   HTTPConnectTimeout,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || NonPublicIP(ip) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		},
	}
}

var nonPublicNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "fc00::/7", "2001:db8::/32",
	} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// NonPublicIP reports whether ip is anything but a routable public address:
// loopback, private, link-local (the cloud metadata address among them),
// unspecified, multicast, CGNAT (Tailscale), NAT64 (which embeds an IPv4
// address that may itself be private) and the other reserved ranges.
func NonPublicIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.Equal(net.IPv4bcast) {
		return true
	}
	for _, n := range nonPublicNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

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

// SourceHook is a research source as research and agents ask it: a search
// connector's entry (Connector names it), with its topics and agent tool.
// It is also the shape a Source Hook was stored in, before Source Hooks
// became connectors, which the move (core/service, hook_migrate.go) and a
// bundle carrying one read.
type SourceHook struct {
	Name     string         `json:"name"`      // Display name (e.g., "Westlaw", "PubMed API")
	Type     SourceHookType `json:"type"`      // api, rag, paywall
	Endpoint string         `json:"endpoint"`  // Base URL for API/RAG, or empty for paywall
	AuthType SourceHookAuth `json:"auth_type"` // none, api_key, bearer
	AuthKey  string         `json:"auth_key"`  // API key or bearer token (stored encrypted)

	// Connector, set only on an entry ConnectorSources makes, is the search
	// connector (core/service) the entry stands for: research asks it on its
	// topics like a hook, through ConnectorSearch. Never stored.
	Connector string `json:"connector,omitempty"`

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

	// CostPerCall, when > 0, prices each real (cache-miss) call to this hook
	// into the admin cost chart + per-source breakdown (a "cost hook"). 0 =
	// untracked (a free endpoint). Recorded via RecordExternalCost.
	CostPerCall float64 `json:"cost_per_call,omitempty"`

	// LLM-tool exposure. When ExposeToLLM is true, the framework
	// auto-generates an AgentToolDef from this hook (via
	// BuildSourceHookAgentToolDefs) so agents can invoke it
	// directly. Default false — admin opts in per hook so
	// internal-only hooks stay invisible to the LLM.
	//
	// Paywall hooks are never auto-exposed regardless of this flag —
	// they augment fetch_url with auth headers, not stand-alone search.
	ExposeToLLM     bool   `json:"expose_to_llm,omitempty"`
	ToolName        string `json:"tool_name,omitempty"`        // LLM-facing tool name (lowercase, underscores). Defaults to "<name>_search" when ExposeToLLM is on but this is empty.
	ToolDescription string `json:"tool_description,omitempty"` // LLM-facing description. Tells the model when to choose this over web_search. Defaults to a terse derived string when empty.

	// Disabled mutes the hook everywhere it's CONSULTED — topic routing
	// (HooksForTopic), paywall matching (HookForDomain), agents' source
	// tools — while it stays visible on management surfaces
	// (admin list, CLI, export). It exists as the bundle-import review gate: an
	// imported hook receives live search queries at its endpoint once active,
	// so it lands disabled until the admin has looked. A local mute, not part
	// of the hook's shape — export clears it.
	Disabled bool `json:"disabled,omitempty"`
}

// ConnectorSources, when set, are the search connectors offered to research
// on topics (core/service installs it), as entries HooksForTopic matches
// like hooks; ConnectorSearch asks one of them.
var (
	ConnectorSources func() []SourceHook
	ConnectorSearch  func(connector, query string) (string, error)
)

// Sources are the search connectors offered beyond Web Search, each as the
// entry research and agents ask: its topics, and its agent tool when it
// offers one. The connector Web Search is set to is not among them: it is
// asked as web_search, so one API is not called twice under two names.
func Sources() []SourceHook {
	if ConnectorSources == nil {
		return nil
	}
	var out []SourceHook
	for _, h := range ConnectorSources() {
		if !h.Disabled && !IsSearchEngine(h) {
			out = append(out, h)
		}
	}
	return out
}

// HooksForTopic returns the sources research asks on the given topic
// domains: those always asked, and those for any of the topics.
func HooksForTopic(topic_domains []string) []SourceHook {
	domain_set := make(map[string]bool)
	for _, d := range topic_domains {
		domain_set[strings.ToLower(d)] = true
	}
	var matched []SourceHook
	for _, hook := range Sources() {
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

// hookCacheDB is the persistent cache for source hook results. When set,
// QuerySourceHook checks this before making API calls. Keyed by hook name
// + normalized query. Skips the API call entirely on cache hit.
// Main package wires this up via SetHookCacheDB after database init.
var (
	hookCacheDB       Store
	hookCacheMu       sync.RWMutex
	hookCacheTTL      = 30 * 24 * time.Hour // 30 days for non-empty results
	hookCacheEmptyTTL = 7 * 24 * time.Hour  // 7 days for empty results (negative cache)
	hookCacheBucket   = "source_hook_cache"
)

// hookCacheStopwords are removed from query strings before computing the
// cache key. Stripping filler words like "and", "or", "the", "a" lets
// queries that differ only by common connectives collide in the cache.
// Stopwords from boolean-style CourtListener queries also get stripped.
// Stopwords is exported because the vector store strips the same words when
// it builds its own keys — one list, so two callers cannot drift apart.
var Stopwords = map[string]bool{
	"and": true, "or": true, "not": true,
	"the": true, "a": true, "an": true,
	"of": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "by": true, "with": true,
	"is": true, "are": true, "as": true,
	"how": true, "what": true, "which": true, "who": true,
}

// SetHookCacheDB wires the persistent cache backing store. Call once during
// startup after the database is open. Passing nil disables caching.
func SetHookCacheDB(db Store) {
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
		if Stopwords[t] || seen[t] {
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
		// Lazy delete-on-read: reclaim the expired entry now rather than
		// letting it linger until a sweep. Cheap — we already have the key.
		db.Unset(hookCacheBucket, key)
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

// SweepHookCache removes expired entries from both the source-hook result
// cache and the authoritative-domain cache. Lazy delete-on-read (in
// lookupHookCache / LookupAuthDomainCache) reclaims entries that get
// re-queried after they expire; this sweep reclaims the long tail of
// entries that are never queried again, keeping the persistent cache
// bounded the way the fetch_url cache stays bounded via its quota eviction.
// Returns the number of entries removed. A stopped ctx ends the sweep at the
// next entry; each removal stands on its own.
func SweepHookCache(ctx context.Context) int {
	hookCacheMu.RLock()
	db := hookCacheDB
	ttl := hookCacheTTL
	emptyTTL := hookCacheEmptyTTL
	hookCacheMu.RUnlock()
	if db == nil {
		return 0
	}
	removed := 0
	// Source-hook entries: empty (negative-cache) results expire on the
	// shorter empty TTL, matching lookupHookCache.
	for _, key := range db.Keys(hookCacheBucket) {
		if ctx.Err() != nil {
			return removed
		}
		var entry hookCacheEntry
		if !db.Get(hookCacheBucket, key, &entry) {
			continue
		}
		effectiveTTL := ttl
		if entry.Result == "" {
			effectiveTTL = emptyTTL
		}
		if time.Since(entry.FetchedAt) > effectiveTTL {
			db.Unset(hookCacheBucket, key)
			removed++
		}
	}
	// Authoritative-domain entries: single TTL.
	for _, key := range db.Keys(authDomainBucket) {
		if ctx.Err() != nil {
			return removed
		}
		var entry authDomainEntry
		if !db.Get(authDomainBucket, key, &entry) {
			continue
		}
		if time.Since(entry.FetchedAt) > ttl {
			db.Unset(authDomainBucket, key)
			removed++
		}
	}
	return removed
}

// hookCacheSweepOnce guards StartHookCacheSweeper so the background goroutine
// is launched at most once even if startup wiring calls it more than once.
var hookCacheSweepOnce sync.Once

// StartHookCacheSweeper launches a background goroutine that sweeps expired
// cache entries once at startup and then daily. Lazy delete-on-read handles
// re-queried entries; this keeps the never-re-queried tail from accumulating.
// Call once during startup after SetHookCacheDB. Safe to call repeatedly.
func StartHookCacheSweeper() {
	hookCacheSweepOnce.Do(func() {
		go func() {
			for {
				if n := SweepHookCache(context.Background()); n > 0 {
					nfo.Log("[cache] swept %d expired source-hook/auth-domain cache entries", n)
				}
				time.Sleep(24 * time.Hour)
			}
		}()
	})
}

// Expose the sweep as an admin-triggered maintenance action too, so an
// operator can reclaim space on demand without waiting for the daily pass.
func init() {
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
		// Lazy delete-on-read, same as lookupHookCache.
		db.Unset(authDomainBucket, key)
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

// QuerySourceHook searches the search connector a source entry names,
// through the persistent caches (when SetHookCacheDB set one), so the same
// or a near query across sessions does not ask the service again. This
// matters for rate-limited and metered services.
func QuerySourceHook(hook SourceHook, query string) (string, error) {
	if hook.Connector == "" {
		return "", fmt.Errorf("%s is not a search connector: Source Hooks became connectors", hook.Name)
	}
	if ConnectorSearch == nil {
		return "", fmt.Errorf("%s is a search connector, and this build has no connectors linked", hook.Connector)
	}
	name := hook.Connector
	return CachedSearch(name, query, func() (string, error) { return ConnectorSearch(name, query) })
}

// CachedSearch answers query for the source called name from its caches,
// else with live, and keeps what live found: research and agents asking the
// same or a near query again are answered without another call.
func CachedSearch(name, query string, live func() (string, error)) (string, error) {
	// Tier 1: exact-key cache lookup. Covers identical normalized
	// queries across runs. An empty cached result is a valid
	// negative-cache hit.
	if cached, ok := lookupHookCache(name, query); ok {
		if cached == "" {
			nfo.Debug("[source-hooks] cache HIT (empty) %s: %q", name, query)
		} else {
			nfo.Debug("[source-hooks] cache HIT %s: %q (%d chars)", name, query, len(cached))
		}
		return cached, nil
	}

	// Tier 2: document-level search over prior cached results.
	// Handles near-miss queries that share vocabulary with cached
	// content but don't exact-match any prior query key. Only the
	// token buckets for this query's tokens get loaded from disk,
	// so the cost stays bounded regardless of total index size.
	if docs := searchHookDocs(name, query, 10); len(docs) > 0 {
		result := formatIndexedDocs(docs)
		nfo.Debug("[source-hooks] FTS HIT %s: %q (%d docs)", name, query, len(docs))
		// Store this as a tier-1 cache entry too, so the next
		// identical query gets served from the fast path.
		storeHookCache(name, query, result)
		return result, nil
	}

	// Tier 3: live.
	nfo.Debug("[source-hooks] cache MISS %s: %q, asking live", name, query)
	result, err := live()
	if err != nil {
		// Errors are NOT cached — they might be transient (rate limit,
		// network issue). Let the next call retry.
		nfo.Debug("[source-hooks] cache SKIP-STORE %s: %q, live error: %v", name, query, err)
		return result, err
	}
	// Store both empty and non-empty results. Empty results use a
	// shorter TTL inside lookupHookCache so they get retried sooner.
	storeHookCache(name, query, result)
	// Populate the tier-2 document index so future near-miss queries
	// can find this content without hitting the API.
	indexHookDocs(name, result)
	if result == "" {
		nfo.Debug("[source-hooks] cache STORE (negative) %s: %q", name, query)
	} else {
		nfo.Debug("[source-hooks] cache STORE %s: %q (%d chars)", name, query, len(result))
	}
	return result, nil
}

// EngineHook names the search connector that is the web search engine, from
// the search settings (core sets it); empty when the engine is not one.
var EngineHook func() string

// IsSearchEngine reports whether h is the web search engine. Picked under
// Web Search, a search connector is asked only as web_search, never routed
// to by topic or offered to agents as a tool, so one API is not called
// twice under two names.
func IsSearchEngine(h SourceHook) bool {
	if EngineHook == nil {
		return false
	}
	n := strings.TrimSpace(EngineHook())
	return n != "" && strings.EqualFold(n, strings.TrimSpace(h.Name))
}
