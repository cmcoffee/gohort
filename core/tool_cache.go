// One run, one answer per question.
//
// A run that fans out asks the same things twice. Research's own engine
// carries a SearchCache for exactly this: eight branches investigating
// eight sub-questions of one topic overlap heavily, and without a cache
// each one pays for the same landscape search, the same fetch of the same
// URL, the same page.
//
// The framework can do that for every run rather than every app doing it
// again, because it already sits between the model and the tool: wrap the
// catalog once, and identical calls inside that run answer from the first
// result. No tool knows about it and no tool has to.
//
// WHAT IS NOT CACHED is the load-bearing half. A cache that deduped a
// send, a write, or a purchase would turn "do it twice" into "do it once"
// silently, which is a correctness bug wearing an optimization's clothes.
// So the gate is deliberately narrow: a tool qualifies only when it says
// it is a reader (Caps within read/network) and does not ask for
// confirmation. Anything unannotated is treated as consequential, because
// a tool that never declared its caps has not promised anything.

package core

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// runCacheMaxEntries bounds one run's cache. A long research run makes
// hundreds of calls, and holding every result for the life of the run is
// how a background job's memory becomes somebody's afternoon. Past the
// cap the cache stops ACCEPTING rather than evicting: the entries already
// there are the ones being hit, and evicting them to make room for a
// one-off is the wrong trade.
const runCacheMaxEntries = 256

// runCacheMaxResultBytes skips caching a result too large to be worth
// holding. A 4MB page fetched once is cheaper to fetch again than to keep
// for a run that may last an hour.
const runCacheMaxResultBytes = 256 << 10

// RunToolCache remembers what a tool answered inside ONE run.
//
// Safe for concurrent use: fanout branches run in parallel and are the
// main reason this exists.
type RunToolCache struct {
	mu      sync.Mutex
	entries map[string]string
	hits    int
}

// NewRunToolCache returns an empty cache for one run.
func NewRunToolCache() *RunToolCache { return &RunToolCache{entries: map[string]string{}} }

// Hits reports how many calls were answered from the cache, for a log line
// that says whether it earned its keep.
func (c *RunToolCache) Hits() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

func (c *RunToolCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	if ok {
		c.hits++
	}
	return v, ok
}

func (c *RunToolCache) put(key, val string) {
	if len(val) > runCacheMaxResultBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= runCacheMaxEntries {
		return
	}
	c.entries[key] = val
}

// WrapToolsWithRunCache returns the catalog with every CACHEABLE tool's
// handler answering repeats from cache. Tools that do not qualify are
// returned untouched, so the caller can wrap a whole catalog without
// sorting it first.
//
// A nil cache returns the catalog unchanged, which is what makes this
// safe to call unconditionally.
func WrapToolsWithRunCache(cache *RunToolCache, tools []AgentToolDef) []AgentToolDef {
	if cache == nil || len(tools) == 0 {
		return tools
	}
	out := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !toolIsCacheable(td) {
			out = append(out, td)
			continue
		}
		name, inner := td.Tool.Name, td.Handler
		td.Handler = func(args map[string]any) (string, error) {
			key := runCacheKey(name, args)
			if v, ok := cache.get(key); ok {
				return v, nil
			}
			v, err := inner(args)
			if err != nil {
				// Errors are never cached. A search that failed on a
				// timeout is a question that has not been answered, and
				// answering it from cache would turn one bad moment into
				// a bad run.
				return v, err
			}
			cache.put(key, v)
			return v, nil
		}
		out = append(out, td)
	}
	return out
}

// toolIsCacheable decides whether repeating a call may be answered from
// the first one.
//
// Read-only in the declared sense, and not confirmable. Deliberately
// conservative in both directions: a tool with no declared caps is NOT
// cacheable (it has promised nothing), and one that asks for confirmation
// is not either (asking twice means it expected to happen twice).
func toolIsCacheable(td AgentToolDef) bool {
	if td.Handler == nil || td.NeedsConfirm || len(td.Tool.Caps) == 0 {
		return false
	}
	for _, c := range td.Tool.Caps {
		if c != CapRead && c != CapNetwork {
			return false
		}
	}
	return true
}

// runCacheKey identifies one call: the tool, and the arguments it was
// given, in a stable order.
//
// Marshalled rather than fmt-printed because Go prints maps in a random
// order for anything but the simplest shapes, and a key that changes
// between two identical calls is a cache that never hits.
func runCacheKey(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte('\x1f')
		b.WriteString(k)
		b.WriteByte('=')
		if enc, err := json.Marshal(args[k]); err == nil {
			b.Write(enc)
			continue
		}
		// Unmarshalable (a channel, a func) means we cannot say two calls
		// are the same, so make the key unique and let the call through.
		return name + "\x1funcacheable"
	}
	return b.String()
}
