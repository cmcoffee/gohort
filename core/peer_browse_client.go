// Choosing WHERE a page gets rendered: this instance's headless browser, or a
// peer's.
//
// The routing is a swap rather than a new call path. tools/browser registers
// the local implementation, core owns BrowserFetchFunc and dispatches on the
// configured source — so browse_page, the sandbox hook's browse_page, and
// find_image's render escalation all follow the setting without any of them
// learning that peers exist. That is the same move the peer image connectors
// make, and for the same reason: teaching every consumer about peers would be a
// much larger change and a much easier one to get wrong.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BrowseConfig records where page rendering happens.
//
// Just the source: unlike search or STT there is nothing to configure locally —
// the browser is either linked into the build or it is not — so this is one
// choice, not a form.
type BrowseConfig struct {
	Source string `json:"source,omitempty"` // "local" (default) or "peer:<name>"
}

var (
	browseMu    sync.RWMutex
	browseCfg   BrowseConfig
	localBrowse func(url string, maxChars int) (string, error)
)

// RegisterBrowserFetch installs the LOCAL page-rendering implementation.
//
// Called by the browser package at init instead of assigning BrowserFetchFunc
// directly, so core keeps ownership of that variable and can route it. A build
// without the browser linked registers nothing and browse stays unavailable —
// which is exactly what it was before.
func RegisterBrowserFetch(fn func(url string, maxChars int) (string, error)) {
	browseMu.Lock()
	localBrowse = fn
	browseMu.Unlock()
	installBrowseRouting()
}

// LoadBrowseConfig returns the stored browse source.
func LoadBrowseConfig() BrowseConfig {
	browseMu.RLock()
	defer browseMu.RUnlock()
	return browseCfg
}

// SetBrowseConfig installs the source and re-points BrowserFetchFunc.
func SetBrowseConfig(cfg BrowseConfig) {
	browseMu.Lock()
	browseCfg = cfg
	browseMu.Unlock()
	installBrowseRouting()
}

// LoadBrowseConfigFromDB restores the stored source at startup.
func LoadBrowseConfigFromDB(db Database) {
	if db == nil {
		return
	}
	var src string
	db.Get(BrowseTable, "source", &src)
	SetBrowseConfig(BrowseConfig{Source: src})
}

// BrowsePeer returns the peer pages are rendered on, if one is selected and
// still offers browsing. Also the relay guard: a serving instance that is
// itself borrowing must not pass the request on.
func BrowsePeer() (RemotePeer, bool) {
	cfg := LoadBrowseConfig()
	src := strings.TrimSpace(cfg.Source)
	if src == "" || src == EmbeddingProviderLocal {
		return RemotePeer{}, false
	}
	p, ok := PeerFromProvider(src)
	if !ok || !p.Offers(PeerCapBrowse) {
		return RemotePeer{}, false
	}
	return p, true
}

// installBrowseRouting points BrowserFetchFunc at whichever renderer the
// current configuration names. Idempotent; safe to call on every change.
func installBrowseRouting() {
	BrowserFetchFunc = func(url string, maxChars int) (string, error) {
		if p, ok := BrowsePeer(); ok {
			return peerBrowseFetch(p, url, maxChars)
		}
		browseMu.RLock()
		fn := localBrowse
		browseMu.RUnlock()
		if fn == nil {
			return "", fmt.Errorf("no browser is available: this build has none linked and no peer is selected for page rendering")
		}
		return fn(url, maxChars)
	}
}

// peerBrowseFetchTimeout bounds the round trip. Longer than the far side's own
// render budget so a slow page comes back as ITS timeout, with its explanation,
// rather than as a bare client-side cutoff here.
const peerBrowseFetchTimeout = 90 * time.Second

// peerBrowseFetch renders a page on a peer.
//
// The non-public-host guard runs HERE too, before anything leaves. The far side
// enforces it as well and that is the one that actually protects the far side's
// network — but refusing locally means an agent asking for 192.168.1.1 gets a
// straight answer instead of a round trip and a remote 400, and it keeps the
// behaviour identical whether browsing is local or borrowed.
func peerBrowseFetch(p RemotePeer, url string, maxChars int) (string, error) {
	if err := RefuseNonPublicHost(url); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"url": url, "max_chars": maxChars})
	ctx, cancel := context.WithTimeout(context.Background(), peerBrowseFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BrowseURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := PeerClientFor(p, peerBrowseFetchTimeout).Do(req)
	if err != nil {
		return "", fmt.Errorf("browsing on peer %s: %w", p.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if strings.TrimSpace(e.Error) == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("peer %s refused the render: %s", p.Name, e.Error)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("peer %s returned an unreadable render: %w", p.Name, err)
	}
	return out.Text, nil
}
