// Web access sharing, serving half: searching and page-rendering on this
// instance's behalf-of-a-peer.
//
// Two capabilities, deliberately separate grants, because they are different
// kinds of resource wearing the same "reach the web" label:
//
//   - SEARCH spends a metered third-party API key. It is the first peer
//     capability that costs money per call rather than electricity, which is
//     why it carries its own rate ceiling (see peerSearchRatePerMin) instead of
//     inheriting one sized for bulk embedding.
//   - BROWSE spends CPU and RAM on a headless Chromium that gohort downloads
//     per install. That is the same story as the GPU capabilities, and the
//     reason a laptop should be able to borrow it.
//
// Search answers in the SearXNG JSON shape for the reason every other peer
// endpoint speaks somebody else's protocol: tools/websearch already drives that
// shape, so a consuming instance points its ordinary web-search config at the
// peer with provider=searxng and needs no client of its own.
package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// peerSearchRatePerMin caps searches per key per minute, independently of the
// general peer rate limit.
//
// The shared ceiling defaults to 600/min, which is reasonable for an embedder
// grinding through a corpus and is a billing incident for a paid search API.
// Nothing else on the peer surface bills per call, so nothing else needed this;
// search does, and inheriting a number chosen for a different resource is how
// an operator discovers the difference from an invoice.
const peerSearchRatePerMin = 20

// peerBrowseBudget bounds one page render. Headless Chromium on a page that
// never settles will wait indefinitely otherwise, and the peer's connection
// waits with it.
const peerBrowseBudget = 60 * time.Second

// peerBrowseMaxChars caps the text handed back. Generous — a script on the far
// side processes bytes, not tokens — but finite.
const peerBrowseMaxChars = 2 << 20

// SearchWithProviderFunc is set by the websearch package so core can run a
// search without importing it. Same seam as BrowserFetchFunc and CrossSearchFunc.
var SearchWithProviderFunc func(query, provider, apiKey, endpoint string) (string, error)

// --- per-capability rate limiting -------------------------------------------

var (
	peerCapRateMu sync.Mutex
	peerCapRate   = map[string][]time.Time{} // "<keyID>:<cap>" -> recent call times
)

// peerCapRateAllow charges a SECOND, capability-specific rate limit on top of
// the key's general one. Returns false when this key has spent its allowance
// for this capability inside the trailing minute.
func peerCapRateAllow(keyID, capability string, perMin int) bool {
	if perMin <= 0 {
		return true
	}
	id := keyID + ":" + capability
	cutoff := time.Now().Add(-time.Minute)
	peerCapRateMu.Lock()
	defer peerCapRateMu.Unlock()
	kept := peerCapRate[id][:0]
	for _, t := range peerCapRate[id] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= perMin {
		peerCapRate[id] = kept
		return false
	}
	peerCapRate[id] = append(kept, time.Now())
	return true
}

// --- manifest ----------------------------------------------------------------

// PeerSearchInfo tells a peer how to reach this instance's search.
//
// Provider is this instance's OWN upstream ("serper", "duckduckgo"), reported
// so an operator can see what they are actually borrowing — a free DuckDuckGo
// relay and a metered Serper key are the same endpoint and very different
// favours. Path is where to point a SearXNG-shaped client.
type PeerSearchInfo struct {
	Provider string `json:"provider,omitempty"`
	Path     string `json:"path"`
	RatePerM int    `json:"rate_per_min,omitempty"`
}

// peerSearchInfo describes this instance's search for the manifest, or nil when
// it has none configured.
func peerSearchInfo(path string) *PeerSearchInfo {
	cfg := LoadWebSearchConfig()
	if strings.TrimSpace(cfg.Provider) == "" || SearchWithProviderFunc == nil {
		return nil
	}
	return &PeerSearchInfo{Provider: cfg.Provider, Path: path, RatePerM: peerSearchRatePerMin}
}

// peerBrowseServed reports whether this build can render pages for a peer. The
// browser package registers BrowserFetchFunc at init; a build without it linked
// must not advertise the capability.
func peerBrowseServed() bool {
	if _, borrowing := BrowsePeer(); borrowing {
		// Borrowed rendering is not ours to sub-let.
		return false
	}
	browseMu.RLock()
	defer browseMu.RUnlock()
	return localBrowse != nil
}

// --- search ------------------------------------------------------------------

// HandlePeerSearch serves GET /api/peer/v1/search?q=… in the SearXNG JSON
// shape, so the far side drives it with an ordinary searxng-provider config.
func HandlePeerSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapSearch)
	if !ok {
		return
	}
	if !peerCapRateAllow(k.ID, PeerCapSearch, peerSearchRatePerMin) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, fmt.Sprintf(
			"search is limited to %d calls per minute per key — it spends a metered API key, so it is capped separately from the general peer rate",
			peerSearchRatePerMin))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		peerDeny(w, http.StatusBadRequest, "q is required")
		return
	}
	cfg := LoadWebSearchConfig()
	if strings.TrimSpace(cfg.Provider) == "" || SearchWithProviderFunc == nil {
		peerDeny(w, http.StatusServiceUnavailable, "this instance has no search provider configured")
		return
	}
	// Refuse to relay, exactly as embeddings and transcription do: an instance
	// borrowing its own search from a peer must not serve it onward, or A→B→A
	// becomes a loop neither side can see.
	if strings.Contains(cfg.Endpoint, "/api/peer/") {
		peerDeny(w, http.StatusServiceUnavailable,
			"this instance borrows its own search from another peer and will not relay it")
		return
	}

	out, err := SearchWithProviderFunc(query, cfg.Provider, cfg.APIKey, cfg.Endpoint)
	if err != nil {
		peerDeny(w, http.StatusBadGateway, "search failed: "+err.Error())
		return
	}
	Debug("[peer] %q searched %q via %s", k.Label, query, cfg.Provider)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": peerSearchResults(out)})
	touchPeerKey(k)
}

// peerSearchResults reshapes the provider-agnostic numbered text every search
// backend returns into the SearXNG result objects the client expects.
//
// The text shape is what tools/websearch produces for the LLM, and it is the
// only shape shared by all five providers — reaching past it would mean
// teaching this endpoint each backend's native JSON, which is exactly the
// coupling the whole peer design avoids.
func peerSearchResults(text string) []map[string]string {
	var out []map[string]string
	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		title := strings.TrimSpace(lines[0])
		// Strip the "1. " ordinal the text form carries.
		if i := strings.Index(title, ". "); i > 0 && i <= 3 {
			title = strings.TrimSpace(title[i+2:])
		}
		if title == "" || title == "No results found." {
			continue
		}
		item := map[string]string{"title": title}
		if len(lines) > 1 {
			item["url"] = strings.TrimSpace(lines[1])
		}
		if len(lines) > 2 {
			item["content"] = strings.TrimSpace(strings.Join(lines[2:], " "))
		}
		out = append(out, item)
	}
	return out
}

// --- browse ------------------------------------------------------------------

// HandlePeerBrowse serves POST /api/peer/v1/browse — render a page in this
// instance's headless browser and return its text.
func HandlePeerBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapBrowse)
	if !ok {
		return
	}
	if BrowserFetchFunc == nil {
		peerDeny(w, http.StatusServiceUnavailable, "this build has no browser linked")
		return
	}
	// Refuse to relay, as embeddings, transcription and search all do. An
	// instance that renders on ANOTHER peer would otherwise pass the request
	// along, and A→B→A is a loop neither side can see — here it would also
	// spend a third machine's browser on a request it never agreed to.
	if p, borrowing := BrowsePeer(); borrowing {
		peerDeny(w, http.StatusServiceUnavailable,
			"this instance renders its own pages on peer "+p.Name+" and will not relay")
		return
	}
	var req struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		peerDeny(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// THE check for this endpoint. The tool layer has always refused non-public
	// hosts, but that guard lived on BrowsePageTool.Run — a peer handler
	// calling BrowserFetchFunc directly would sail straight past it, and this
	// instance is precisely the one sitting inside a network the caller cannot
	// otherwise reach. Without this, a peer key is a LAN proxy.
	if err := RefuseNonPublicHost(req.URL); err != nil {
		peerDeny(w, http.StatusBadRequest, err.Error()+
			" — a peer may only browse the public web through this instance, never its private network")
		return
	}
	max := req.MaxChars
	if max <= 0 || max > peerBrowseMaxChars {
		max = peerBrowseMaxChars
	}

	done := make(chan struct{})
	var text string
	var ferr error
	go func() {
		defer close(done)
		text, ferr = BrowserFetchFunc(req.URL, max)
	}()
	select {
	case <-done:
	case <-time.After(peerBrowseBudget):
		peerDeny(w, http.StatusGatewayTimeout, "the page did not finish rendering within the budget")
		return
	case <-r.Context().Done():
		peerDeny(w, http.StatusRequestTimeout, "the caller went away")
		return
	}
	if ferr != nil {
		peerDeny(w, http.StatusBadGateway, "browse failed: "+ferr.Error())
		return
	}
	Debug("[peer] %q browsed %q (%d chars)", k.Label, req.URL, len(text))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"url": req.URL, "text": text})
	touchPeerKey(k)
}

// --- consuming half ----------------------------------------------------------

// SearchURL is where a peer's SearXNG-shaped search client should point.
func (p RemotePeer) SearchURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1"
}

// BrowseURL is the peer's page-rendering endpoint.
func (p RemotePeer) BrowseURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1/browse"
}

// ResolveSearchProvider turns a submitted web-search config into the one to
// store. A peer selection becomes an ordinary searxng-provider config pointed
// at that peer, with the peer key as the bearer — so tools/websearch keeps
// working with no knowledge that a peer exists.
func ResolveSearchProvider(cfg WebSearchConfig, provider string) (WebSearchConfig, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == EmbeddingProviderLocal {
		return cfg, nil
	}
	p, ok := PeerFromProvider(provider)
	if !ok {
		return cfg, fmt.Errorf("no peer named %q is registered — add it under Peers first",
			strings.TrimPrefix(provider, peerProviderPrefix))
	}
	if !p.Offers(PeerCapSearch) {
		return cfg, fmt.Errorf("peer %q does not offer search (it offers: %s)",
			p.Name, strings.Join(p.Caps, ", "))
	}
	cfg.Provider = "searxng"
	cfg.Endpoint = p.SearchURL()
	cfg.APIKey = p.Key
	return cfg, nil
}
