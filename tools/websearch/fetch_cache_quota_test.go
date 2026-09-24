package websearch

// The admin panel documents a fetch cache quota of 0 as "disables caching".
// It did not: a stored 0 read as unset and came back as the 100MB default, and
// the evictor, the only reader of the quota, took 0 to mean nothing to enforce.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestFetchCacheQuotaZeroWritesNothing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	defer func() { AuthDB = prev }()

	// Unset is the default, and the cache works.
	if got := FetchCacheQuotaBytes(); got != 100*1024*1024 {
		t.Fatalf("unset quota = %d, want the 100MB default", got)
	}
	dir := t.TempDir()
	if _, _, _, err := writeCacheString("https://example.com/a", dir, "full text", "text/html"); err != nil {
		t.Fatalf("a cache write with the default quota: %v", err)
	}

	// 0 is a choice, not an absence.
	db.Set(WebTable, "fetch_cache_quota_mb", 0)
	if got := FetchCacheQuotaBytes(); got != 0 {
		t.Fatalf("a stored 0 reads as %d bytes; it has to disable the cache", got)
	}
	off := t.TempDir()
	if _, _, _, err := writeCacheString("https://example.com/b", off, "full text", "text/html"); !errors.Is(err, errFetchCacheOff) {
		t.Errorf("a cache write with the cache off: %v", err)
	}
	if _, _, _, err := fetchAndCache("https://example.com/c", off, "application/pdf"); !errors.Is(err, errFetchCacheOff) {
		t.Errorf("a binary auto-cache with the cache off: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(off, ".fetch_cache")); len(entries) != 0 {
		t.Errorf("the cache is off and still holds %d file(s)", len(entries))
	}

	// And a real number is honoured.
	db.Set(WebTable, "fetch_cache_quota_mb", 5)
	if got := FetchCacheQuotaBytes(); got != 5*1024*1024 {
		t.Errorf("quota 5 reads as %d bytes", got)
	}
}
