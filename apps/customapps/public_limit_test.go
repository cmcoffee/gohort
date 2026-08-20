// The anonymous capability surface runs the OWNER'S sandboxed script on every
// data request, and the query params that key its output cache come from
// whoever holds the link. Unthrottled that was one subprocess per request and
// one cache entry per distinct param set, neither of which had a ceiling.
package customapps

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// fromAddr builds a request whose TCP peer is addr.
func fromAddr(target, addr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = addr
	return r
}

func TestPublicSurfaceThrottlesOneSource(t *testing.T) {
	prev := publicAppRequests
	publicAppRequests = NewRateLimiter(3, time.Minute)
	t.Cleanup(func() { publicAppRequests = prev })

	T := &CustomApps{}
	// An unknown token 404s, which is fine: what is being pinned is that the
	// ceiling applies before the work, not that the app resolves.
	refused := 0
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		T.handlePublic(w, fromAddr("/custom/pub/nope/", "203.0.113.5:5555"), "/nope/")
		if w.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("the anonymous surface never refused a burst from one source")
	}
	if refused != 7 {
		t.Errorf("expected 3 allowed then 7 refused, got %d refused", refused)
	}
}

func TestPublicThrottleIsPerSource(t *testing.T) {
	prev := publicAppRequests
	publicAppRequests = NewRateLimiter(1, time.Minute)
	t.Cleanup(func() { publicAppRequests = prev })

	T := &CustomApps{}
	w := httptest.NewRecorder()
	T.handlePublic(w, fromAddr("/custom/pub/nope/", "203.0.113.5:5555"), "/nope/")
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("the first request from a source was refused")
	}
	// A different address has its own budget, so one noisy caller does not
	// take the app down for everyone.
	w = httptest.NewRecorder()
	T.handlePublic(w, fromAddr("/custom/pub/nope/", "198.51.100.9:5555"), "/nope/")
	if w.Code == http.StatusTooManyRequests {
		t.Error("a second source was refused on the first source's budget")
	}
}

func TestPublicThrottleIgnoresForwardedFor(t *testing.T) {
	// Rotating a header must not buy a fresh budget.
	prev := publicAppRequests
	publicAppRequests = NewRateLimiter(2, time.Minute)
	t.Cleanup(func() { publicAppRequests = prev })

	T := &CustomApps{}
	refused := 0
	for i := 0; i < 8; i++ {
		r := fromAddr("/custom/pub/nope/", "203.0.113.5:5555")
		r.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('1'+i)))
		w := httptest.NewRecorder()
		T.handlePublic(w, r, "/nope/")
		if w.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("rotating X-Forwarded-For walked around the ceiling")
	}
}

func TestDataSourceCacheIsBounded(t *testing.T) {
	// The key includes the caller's query params, so on the public surface a
	// varied param both misses the cache and writes an entry. The sweep only
	// removes EXPIRED entries, so without a hard cap the map grows for as long
	// as requests keep arriving inside the TTL.
	dsCacheMu.Lock()
	dsCache = map[string]dsCacheEntry{}
	for i := 0; i < maxDSCacheEntries*3; i++ {
		dsCache[string(rune(i))+"-k"] = dsCacheEntry{out: "x", expires: time.Now().Add(time.Hour)}
	}
	over := len(dsCache)
	dsCacheMu.Unlock()

	if over <= maxDSCacheEntries {
		t.Skip("fixture did not exceed the cap")
	}
	// Drive one cached run so the eviction path executes.
	dsCacheMu.Lock()
	trimDSCache(time.Now())
	dsCacheMu.Unlock()

	dsCacheMu.Lock()
	size := len(dsCache)
	dsCacheMu.Unlock()
	if size > maxDSCacheEntries {
		t.Errorf("cache stayed above its cap: %d > %d", size, maxDSCacheEntries)
	}
}
