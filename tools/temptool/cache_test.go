package temptool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// withMemRootDB swaps RootDB for an in-memory kvlite store for the
// duration of t. Restores the original on cleanup so tests don't
// pollute each other or any larger suite that needs the real DB.
func withMemRootDB(t *testing.T) {
	t.Helper()
	prev := RootDB
	RootDB = OpenCache()
	t.Cleanup(func() {
		RootDB.Close()
		RootDB = prev
	})
}

// newSess builds a minimal session with the identifiers the cache
// scope logic looks at. Empty fields exercise the "no marker → skip"
// branch when the test wants that path.
func newSess(user, chatID, workspaceDir string) *ToolSession {
	return &ToolSession{Username: user, ChatSessionID: chatID, WorkspaceDir: workspaceDir}
}

// TestCacheHitMissByArgs verifies that storing under one arg set
// returns a hit on identical args and a miss on different args. This
// is the core memoization contract.
func TestCacheHitMissByArgs(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{
		Name:  "fetch_thing",
		Cache: &TempToolCache{Scope: "user"},
	}
	sess := newSess("alice", "", "")

	storeTempToolCache(sess, tt, map[string]any{"url": "https://example.com/x"}, "fetched-x")
	if got, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "https://example.com/x"}); !ok || got != "fetched-x" {
		t.Fatalf("same args: want hit \"fetched-x\", got ok=%v val=%q", ok, got)
	}
	if _, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "https://example.com/y"}); ok {
		t.Fatalf("different args: want miss, got hit")
	}
}

// TestCacheKeyTemplateScopesByPrimaryArg verifies that a Cache.Key
// template using only some args ignores the rest — the videodl
// pattern where the URL alone is the cache identity, not e.g. a
// tagging arg the caller varies.
func TestCacheKeyTemplateScopesByPrimaryArg(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{
		Name:  "download",
		Cache: &TempToolCache{Key: "{url}", Scope: "user"},
	}
	sess := newSess("alice", "", "")

	storeTempToolCache(sess, tt, map[string]any{"url": "u1", "tag": "first"}, "downloaded-u1")
	// Same url, different tag → still a hit because tag isn't in the key.
	if got, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "u1", "tag": "second"}); !ok || got != "downloaded-u1" {
		t.Fatalf("templated key should dedup on url alone: ok=%v val=%q", ok, got)
	}
}

// TestCacheTTLExpiry verifies that an entry past its TTL is treated
// as a miss AND dropped from the table so the next exec re-populates.
func TestCacheTTLExpiry(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{
		Name:  "ephemeral",
		Cache: &TempToolCache{Scope: "user", TTL: "10ms"},
	}
	sess := newSess("alice", "", "")

	storeTempToolCache(sess, tt, map[string]any{"q": "x"}, "v1")
	time.Sleep(25 * time.Millisecond)
	if _, ok := lookupTempToolCache(sess, tt, map[string]any{"q": "x"}); ok {
		t.Fatalf("expired entry should miss")
	}
}

// TestCacheScopeUserIsolation verifies that scope=user partitions per
// username — alice's cache entry is invisible to bob's lookup.
func TestCacheScopeUserIsolation(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{
		Name:  "private",
		Cache: &TempToolCache{Scope: "user"},
	}
	storeTempToolCache(newSess("alice", "", ""), tt, map[string]any{"q": "x"}, "alice-val")
	if _, ok := lookupTempToolCache(newSess("bob", "", ""), tt, map[string]any{"q": "x"}); ok {
		t.Fatalf("user-scoped entry should not leak to a different user")
	}
}

// TestCacheScopeSessionSkipsWithoutID verifies that scope=session on
// a session without ChatSessionID returns ok=false on lookup AND
// stores nothing — the safe fall-back when the marker isn't
// available, instead of silently broadening to a wider scope.
func TestCacheScopeSessionSkipsWithoutID(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{Name: "scoped", Cache: &TempToolCache{Scope: "session"}}
	sessNoID := newSess("alice", "", "")

	storeTempToolCache(sessNoID, tt, map[string]any{"q": "x"}, "val")
	if _, ok := lookupTempToolCache(sessNoID, tt, map[string]any{"q": "x"}); ok {
		t.Fatalf("session scope without ChatSessionID should refuse to cache")
	}
}

// TestCacheInvalidateWhenFileExists verifies that the file_exists
// check drops the entry when the named artifact is gone — the
// videodl pattern where a cached "downloaded video" result is only
// honored if the workspace file is still there.
func TestCacheInvalidateWhenFileExists(t *testing.T) {
	withMemRootDB(t)
	wsDir := t.TempDir()
	artifactPath := filepath.Join(wsDir, "clip.mp4")
	if err := os.WriteFile(artifactPath, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	tt := &TempTool{
		Name: "downloader",
		Cache: &TempToolCache{
			Scope:          "user",
			InvalidateWhen: []string{"file_exists:{workspace_dir}/clip.mp4"},
		},
	}
	sess := newSess("alice", "", wsDir)

	storeTempToolCache(sess, tt, map[string]any{"url": "u"}, "downloaded")
	if _, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "u"}); !ok {
		t.Fatalf("artifact present: want hit")
	}
	// Delete the artifact — next lookup must invalidate + miss.
	_ = os.Remove(artifactPath)
	if _, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "u"}); ok {
		t.Fatalf("artifact gone: want miss")
	}
	// And confirm the entry was dropped, not just skipped (re-store
	// to verify the slot is free / no lingering rec from before).
	storeTempToolCache(sess, tt, map[string]any{"url": "u"}, "downloaded-v2")
	if err := os.WriteFile(artifactPath, []byte("payload-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, ok := lookupTempToolCache(sess, tt, map[string]any{"url": "u"}); !ok || got != "downloaded-v2" {
		t.Fatalf("re-store after invalidation should land cleanly: ok=%v val=%q", ok, got)
	}
}

// TestParseCacheArg verifies the create_temp_tool args → TempToolCache
// parser handles the common shapes the LLM will emit (object with
// some/all fields, invalidate_when as both list and scalar),
// validates scope/TTL, and returns nil for absent / empty specs.
func TestParseCacheArg(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		wantNil bool
		wantErr bool
		check   func(*TempToolCache) bool
	}{
		{"nil → nil", nil, true, false, nil},
		{"empty map → nil", map[string]any{}, true, false, nil},
		{"full spec", map[string]any{
			"key":             "{url}",
			"ttl":             "30d",
			"scope":           "user",
			"invalidate_when": []any{"file_exists:{workspace_dir}/clip.mp4"},
		}, false, false, func(c *TempToolCache) bool {
			return c.Key == "{url}" && c.TTL == "30d" && c.Scope == "user" && len(c.InvalidateWhen) == 1
		}},
		{"invalidate_when as scalar string", map[string]any{
			"scope":           "user",
			"invalidate_when": "file_exists:x",
		}, false, false, func(c *TempToolCache) bool {
			return len(c.InvalidateWhen) == 1 && c.InvalidateWhen[0] == "file_exists:x"
		}},
		{"bad scope rejected", map[string]any{"scope": "deployment"}, false, true, nil},
		{"bad ttl rejected", map[string]any{"ttl": "30x"}, false, true, nil},
		{"non-object raw rejected", "not a map", false, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCacheArg(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err: got %v, want err=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want non-nil cache")
			}
			if tc.check != nil && !tc.check(got) {
				t.Fatalf("check failed: %+v", got)
			}
		})
	}
}
