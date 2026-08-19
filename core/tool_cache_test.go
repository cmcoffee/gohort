package core

// The run-scoped tool cache. The tests that matter are the ones about
// what it REFUSES to cache: deduping a send turns "do it twice" into "do
// it once" silently, which is a correctness bug wearing an optimization's
// clothes.

import (
	"strings"
	"sync"
	"testing"
)

func countingTool(name string, caps []Capability, calls *int) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{Name: name, Caps: caps},
		Handler: func(args map[string]any) (string, error) {
			*calls++
			return "result for " + name, nil
		},
	}
}

func TestRunCacheAnswersARepeatFromTheFirstCall(t *testing.T) {
	calls := 0
	cache := NewRunToolCache()
	tools := WrapToolsWithRunCache(cache, []AgentToolDef{
		countingTool("web_search", []Capability{CapRead, CapNetwork}, &calls),
	})
	args := map[string]any{"query": "what changed", "limit": 5}

	first, err := tools[0].Handler(args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tools[0].Handler(map[string]any{"limit": 5, "query": "what changed"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("the same question was asked twice, got %d call(s)", calls)
	}
	if first != second {
		t.Errorf("the cached answer should be the first one: %q vs %q", first, second)
	}
	if cache.Hits() != 1 {
		t.Errorf("hits = %d, want 1 — the count is how somebody sees it earned its keep", cache.Hits())
	}
	// Different arguments are a different question.
	if _, err := tools[0].Handler(map[string]any{"query": "something else"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("a different question should reach the tool, got %d call(s)", calls)
	}
}

// The load-bearing half.
func TestRunCacheRefusesAnythingConsequential(t *testing.T) {
	cases := map[string]AgentToolDef{
		"a writer":      {Tool: Tool{Name: "save_file", Caps: []Capability{CapRead, CapWrite}}},
		"an executor":   {Tool: Tool{Name: "run_local", Caps: []Capability{CapExecute}}},
		"unannotated":   {Tool: Tool{Name: "mystery"}},
		"a confirmable": {Tool: Tool{Name: "send_message", Caps: []Capability{CapNetwork}}, NeedsConfirm: true},
	}
	for what, td := range cases {
		calls := 0
		td.Handler = func(map[string]any) (string, error) { calls++; return "done", nil }
		tools := WrapToolsWithRunCache(NewRunToolCache(), []AgentToolDef{td})
		args := map[string]any{"to": "alice", "text": "hello"}
		if _, err := tools[0].Handler(args); err != nil {
			t.Fatal(err)
		}
		if _, err := tools[0].Handler(args); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Errorf("%s: asked twice, ran %d time(s) — deduping this is a correctness bug", what, calls)
		}
	}
}

// A search that failed on a timeout is a question that has not been
// answered; caching that would turn one bad moment into a bad run.
func TestRunCacheNeverCachesAFailure(t *testing.T) {
	calls := 0
	td := AgentToolDef{
		Tool: Tool{Name: "fetch_url", Caps: []Capability{CapRead, CapNetwork}},
		Handler: func(map[string]any) (string, error) {
			calls++
			if calls == 1 {
				return "", Error("timed out")
			}
			return "the page", nil
		},
	}
	tools := WrapToolsWithRunCache(NewRunToolCache(), []AgentToolDef{td})
	args := map[string]any{"url": "https://example.com"}
	if _, err := tools[0].Handler(args); err == nil {
		t.Fatal("the first call should have failed")
	}
	out, err := tools[0].Handler(args)
	if err != nil || out != "the page" {
		t.Errorf("a retry should reach the tool again: %q / %v", out, err)
	}
}

// Fanout branches share one cache and run at once, which is the whole
// reason it exists.
func TestRunCacheIsSafeUnderParallelBranches(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	td := AgentToolDef{
		Tool: Tool{Name: "web_search", Caps: []Capability{CapRead, CapNetwork}},
		Handler: func(map[string]any) (string, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return "results", nil
		},
	}
	tools := WrapToolsWithRunCache(NewRunToolCache(), []AgentToolDef{td})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools[0].Handler(map[string]any{"query": "the same thing"})
		}()
	}
	wg.Wait()
	// Not asserting exactly one: sixteen goroutines can race past an empty
	// cache before the first result lands, and serializing them to prevent
	// that would make the cache a bottleneck. Far fewer than sixteen is
	// the guarantee worth having.
	if calls > 8 {
		t.Errorf("the cache should have absorbed most of the burst, got %d call(s)", calls)
	}
}

// A nil cache is the no-op, so a caller can wrap unconditionally.
func TestRunCacheNilIsANoOp(t *testing.T) {
	calls := 0
	in := []AgentToolDef{countingTool("web_search", []Capability{CapRead}, &calls)}
	out := WrapToolsWithRunCache(nil, in)
	if len(out) != 1 {
		t.Fatal("the catalog should come back whole")
	}
	out[0].Handler(nil)
	out[0].Handler(nil)
	if calls != 2 {
		t.Errorf("without a cache every call reaches the tool, got %d", calls)
	}
}

// A key that changes between two identical calls is a cache that never
// hits, which is the failure map ordering causes.
func TestRunCacheKeyIsStableAcrossArgumentOrder(t *testing.T) {
	a := runCacheKey("t", map[string]any{"x": 1, "y": []any{"a", "b"}, "z": map[string]any{"k": true}})
	b := runCacheKey("t", map[string]any{"z": map[string]any{"k": true}, "y": []any{"a", "b"}, "x": 1})
	if a != b {
		t.Errorf("the same call produced two keys:\n%s\n%s", a, b)
	}
	if !strings.HasPrefix(a, "t") {
		t.Errorf("the key should name its tool: %s", a)
	}
	if runCacheKey("t", nil) != "t" {
		t.Error("a no-argument call keys on the tool alone")
	}
}
