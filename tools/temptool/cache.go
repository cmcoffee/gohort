package temptool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// tempToolCacheTable holds memoized temp-tool results. One row per
// (toolName, scope marker, rendered key). Lives in RootDB so caching
// survives restarts and (when scope=user|global) follows the user /
// deployment across conversations.
const tempToolCacheTable = "temptool_cache"

// tempToolCacheRecord is the stored payload. Result is the tool's
// stdout text exactly as the LLM saw it on the original successful
// dispatch; StoredAt drives TTL expiry.
type tempToolCacheRecord struct {
	Result   string    `json:"result"`
	StoredAt time.Time `json:"stored_at"`
}

// lookupTempToolCache returns the memoized result for (tt, args) when
// a valid entry exists. Returns ("", false) on miss OR when any of the
// spec's invariants fails (TTL exceeded, invalidate_when verification
// fails). On invariant failure the stale entry is dropped so the next
// exec re-populates cleanly.
func lookupTempToolCache(sess *ToolSession, tt *TempTool, args map[string]any) (string, bool) {
	if tt == nil || tt.Cache == nil || RootDB == nil {
		return "", false
	}
	rendered, err := renderCacheKey(tt, args)
	if err != nil {
		Debug("[temptool] %q cache key render failed: %v — skipping cache", tt.Name, err)
		return "", false
	}
	marker, ok := cacheScopeMarker(sess, tt.Cache.Scope)
	if !ok {
		return "", false
	}
	storeKey := tempToolCacheStoreKey(tt.Name, marker, rendered)
	var rec tempToolCacheRecord
	if !RootDB.Get(tempToolCacheTable, storeKey, &rec) {
		return "", false
	}
	if ttl, hasTTL := parseCacheTTL(tt.Cache.TTL); hasTTL && ttl > 0 && time.Since(rec.StoredAt) > ttl {
		Debug("[temptool] %q cache hit expired (age=%s, ttl=%s) — dropping", tt.Name, time.Since(rec.StoredAt), ttl)
		RootDB.Unset(tempToolCacheTable, storeKey)
		return "", false
	}
	for _, expr := range tt.Cache.InvalidateWhen {
		if !cacheInvalidateCheckPasses(sess, tt, args, expr) {
			Debug("[temptool] %q cache invalidated by %q — dropping", tt.Name, expr)
			RootDB.Unset(tempToolCacheTable, storeKey)
			return "", false
		}
	}
	Debug("[temptool] %q cache HIT (scope=%s, age=%s)", tt.Name, marker, time.Since(rec.StoredAt))
	return rec.Result, true
}

// storeTempToolCache writes the just-produced result into the cache
// under (tt, args). No-op when the cache spec is absent, when the
// scope marker isn't available on this session, or when the key
// template fails to render. Best-effort — caching is a latency
// optimization, never a correctness contract.
func storeTempToolCache(sess *ToolSession, tt *TempTool, args map[string]any, result string) {
	if tt == nil || tt.Cache == nil || RootDB == nil {
		return
	}
	rendered, err := renderCacheKey(tt, args)
	if err != nil {
		return
	}
	marker, ok := cacheScopeMarker(sess, tt.Cache.Scope)
	if !ok {
		return
	}
	RootDB.Set(tempToolCacheTable, tempToolCacheStoreKey(tt.Name, marker, rendered), tempToolCacheRecord{
		Result:   result,
		StoredAt: time.Now(),
	})
}

// tempToolCacheStoreKey composes the storage key. \x1f (unit
// separator) keeps the parts unambiguously delimited even if a tool
// name, scope marker, or rendered key string contains a colon, slash,
// or any other punctuation we might otherwise have used.
func tempToolCacheStoreKey(toolName, scopeMarker, renderedKey string) string {
	return toolName + "\x1f" + scopeMarker + "\x1f" + renderedKey
}

// cacheScopeMarker resolves the spec's scope to a concrete prefix.
// Empty scope defaults to "user". When the required identifier is
// missing on the session (sessionless CLI, anonymous request, etc.)
// we return ok=false so the caller skips caching rather than silently
// broadening to a less-restrictive scope.
func cacheScopeMarker(sess *ToolSession, scope string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "session":
		if sess == nil || sess.ChatSessionID == "" {
			return "", false
		}
		return "session:" + sess.ChatSessionID, true
	case "user", "":
		if sess == nil || sess.Username == "" {
			return "", false
		}
		return "user:" + sess.Username, true
	case "global":
		return "global", true
	default:
		return "", false
	}
}

// renderCacheKey produces the cache key string for (tt, args). When
// Cache.Key is set it's rendered as a {param}-template (raw text, no
// shell quoting — this is a database key, not a shell command).
// Otherwise we hash the canonical JSON of args so semantically-
// identical calls land on the same entry regardless of map iteration
// order.
func renderCacheKey(tt *TempTool, args map[string]any) (string, error) {
	if tt.Cache != nil && strings.TrimSpace(tt.Cache.Key) != "" {
		return substituteRawTemplate(tt.Cache.Key, args), nil
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		pairs = append(pairs, k, args[k])
	}
	blob, err := json.Marshal(pairs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), nil
}

// substituteRawTemplate replaces {name} placeholders with stringified
// arg values, no shell quoting. Used for cache keys and invalidate-
// when path templates where the result is consumed by Go code, not
// a shell.
func substituteRawTemplate(template string, args map[string]any) string {
	out := template
	for k, v := range args {
		out = strings.ReplaceAll(out, "{"+k+"}", fmt.Sprintf("%v", v))
	}
	return out
}

// parseCacheTTL parses simple duration strings. Recognized:
//
//	"30d" / "1d"  — days (time.ParseDuration doesn't accept these)
//	"12h"         — and any other Go-duration form (h/m/s/ms/...)
//	""            — no TTL (returned as hasTTL=false; entry never
//	                expires by time)
//
// Returns (duration, hasTTL). On parse error returns (0, false) and
// the caller treats it as no TTL — a malformed spec shouldn't make
// the cache silently misbehave.
func parseCacheTTL(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "d") {
		hours := strings.TrimSuffix(s, "d") + "h"
		if d, err := time.ParseDuration(hours); err == nil {
			return d * 24, true
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	return 0, false
}

// parseCacheArg extracts a TempToolCache spec from create_temp_tool's
// raw args["cache"] value. Returns (nil, nil) when no spec is present
// so the caller leaves tool.Cache unset (caching disabled — the
// default). Validates scope + TTL up front so a bad spec fails at
// authoring rather than silently never caching at dispatch.
func parseCacheArg(raw any) (*TempToolCache, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("cache must be a JSON object (got %T)", raw)
	}
	if len(m) == 0 {
		return nil, nil
	}
	c := &TempToolCache{
		Key:   strings.TrimSpace(cacheString(m["key"])),
		TTL:   strings.TrimSpace(cacheString(m["ttl"])),
		Scope: strings.TrimSpace(cacheString(m["scope"])),
	}
	if invs, ok := m["invalidate_when"]; ok {
		for _, v := range cacheStringSlice(invs) {
			if v = strings.TrimSpace(v); v != "" {
				c.InvalidateWhen = append(c.InvalidateWhen, v)
			}
		}
	}
	switch strings.ToLower(c.Scope) {
	case "", "user", "session", "global":
	default:
		return nil, fmt.Errorf("cache.scope must be \"user\", \"session\", or \"global\" (got %q)", c.Scope)
	}
	if c.TTL != "" {
		if _, ok := parseCacheTTL(c.TTL); !ok {
			return nil, fmt.Errorf("cache.ttl must be a duration like \"30d\", \"12h\", or \"30m\" (got %q)", c.TTL)
		}
	}
	return c, nil
}

// cacheString converts a JSON-decoded value to a string. nil → "";
// already-a-string stays; anything else gets fmt-stringified so a
// numeric ttl like 30 becomes "30" rather than dropping silently.
func cacheString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// cacheStringSlice coerces a JSON-decoded value into a string slice
// for invalidate_when. Accepts []string, []any, or a bare string
// (single-item shorthand).
func cacheStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, cacheString(e))
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}

// cacheInvalidateCheckPasses runs one invalidate_when expression and
// returns true if the cache entry should remain valid. Today supports:
//
//	"file_exists:<path-template>"  — rendered path must exist
//
// Unknown kinds return true (lenient — don't invalidate over an
// expression the author meant for a future framework version).
func cacheInvalidateCheckPasses(sess *ToolSession, tt *TempTool, args map[string]any, expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	parts := strings.SplitN(expr, ":", 2)
	if len(parts) != 2 {
		return true
	}
	kind, body := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	switch kind {
	case "file_exists":
		ws := ""
		if sess != nil {
			ws = sess.WorkspaceDir
		}
		body = strings.ReplaceAll(body, "{workspace_dir}", ws)
		rendered := substituteRawTemplate(body, args)
		if rendered == "" {
			return false
		}
		_, err := os.Stat(rendered)
		return err == nil
	}
	return true
}
