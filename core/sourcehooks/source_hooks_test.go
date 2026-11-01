package sourcehooks

import (
	"errors"
	"testing"
)

// A source asked again for the same or a near query is answered from its
// caches; what failed is not kept, so the next ask goes live again. A search
// connector asked as a source shares the same caches under its own name.
func TestCachedSearchAnswersRepeatsFromItsCache(t *testing.T) {
	SetHookCacheDB(newMemStore())
	t.Cleanup(func() { SetHookCacheDB(nil) })
	calls := 0
	live := func() (string, error) { calls++; return "1. Gohort\n   https://a.example/1\n   About gohort.\n\n", nil }
	for _, q := range []string{"gohort agents", "agents gohort", "the gohort agents"} {
		if out, err := CachedSearch("finder", q, live); err != nil || out == "" {
			t.Fatalf("%q: %q %v", q, out, err)
		}
	}
	if calls != 1 {
		t.Errorf("asked live %d times for one query said three ways", calls)
	}
	fails := 0
	failing := func() (string, error) { fails++; return "", errors.New("rate limited") }
	CachedSearch("finder", "something else", failing)
	CachedSearch("finder", "something else", failing)
	if fails != 2 {
		t.Errorf("a failure was kept: asked %d times", fails)
	}

	saved := ConnectorSearch
	t.Cleanup(func() { ConnectorSearch = saved })
	asked := 0
	ConnectorSearch = func(name, query string) (string, error) {
		asked++
		return "1. " + name + "\n   https://b.example/1\n\n", nil
	}
	h := SourceHook{Name: "brave-search", Connector: "brave-search"}
	QuerySourceHook(h, "court rulings on drones")
	if out, _ := QuerySourceHook(h, "drones court rulings"); asked != 1 || out == "" {
		t.Errorf("a connector asked again went live: %d calls, %q", asked, out)
	}
}
