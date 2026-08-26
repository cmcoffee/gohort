package core

import (
	"context"
	"sync/atomic"
	"testing"
)

// countingSource records whether the expensive path was taken.
type countingSource struct {
	kind      string
	items     int
	listCalls int32
	hasCalls  int32
	cheap     bool
}

func (c *countingSource) Kind() string  { return c.kind }
func (c *countingSource) Label() string { return c.kind }
func (c *countingSource) List(user string) []ReferenceItem {
	atomic.AddInt32(&c.listCalls, 1)
	out := make([]ReferenceItem, c.items)
	for i := range out {
		out[i] = ReferenceItem{ID: "i", Name: "n"}
	}
	return out
}
func (c *countingSource) Fetch(ctx context.Context, user, itemID, query string) string { return "" }

// hasItemsSource additionally implements the cheap half.
type hasItemsSource struct{ *countingSource }

func (h hasItemsSource) HasItems(user string) bool {
	atomic.AddInt32(&h.hasCalls, 1)
	return h.items > 0
}

// Asking whether a source has anything must not build what it has.
//
// The filestore source describes each store by WALKING ITS TREE — stat every
// file under every folder — to render "· 12 folders" in a picker. Asking it
// merely whether any store exists therefore cost a full filesystem walk,
// measured live at 2.3 then 5.1 seconds on a page render that displayed none
// of it.
func TestTheCheapQuestionTakesTheCheapPath(t *testing.T) {
	prev := snapshotRefSources()
	defer restoreRefSources(prev)
	resetRefSources()

	cheap := hasItemsSource{&countingSource{kind: "files", items: 3}}
	RegisterReferenceSource(cheap)

	if !AnyReferenceSource("u") {
		t.Fatal("a source with items reported none")
	}
	if got := atomic.LoadInt32(&cheap.listCalls); got != 0 {
		t.Errorf("List was called %d time(s) to answer a yes/no question — that is the tree walk this exists to avoid", got)
	}
	if got := atomic.LoadInt32(&cheap.hasCalls); got != 1 {
		t.Errorf("HasItems called %d times, want 1", got)
	}
}

// A source that does NOT implement the cheap half is still asked the long way
// — correct for the ones whose List is already cheap.
func TestASourceWithoutTheCheapHalfStillWorks(t *testing.T) {
	prev := snapshotRefSources()
	defer restoreRefSources(prev)
	resetRefSources()

	plain := &countingSource{kind: "agents", items: 2}
	RegisterReferenceSource(plain)

	if !AnyReferenceSource("u") {
		t.Fatal("a source with items reported none")
	}
	if got := atomic.LoadInt32(&plain.listCalls); got != 1 {
		t.Errorf("List called %d times, want 1", got)
	}
}

// Empty means empty, by either path.
func TestNoItemsIsReportedAsNone(t *testing.T) {
	prev := snapshotRefSources()
	defer restoreRefSources(prev)
	resetRefSources()

	RegisterReferenceSource(hasItemsSource{&countingSource{kind: "files"}})
	RegisterReferenceSource(&countingSource{kind: "agents"})

	if AnyReferenceSource("u") {
		t.Fatal("two empty sources reported items")
	}
}

// ReferenceGroups still returns the full catalog — the cheap path is for the
// yes/no question only, and a picker must still get descriptions.
func TestGroupsStillBuildTheFullCatalog(t *testing.T) {
	prev := snapshotRefSources()
	defer restoreRefSources(prev)
	resetRefSources()

	src := hasItemsSource{&countingSource{kind: "files", items: 2}}
	RegisterReferenceSource(src)

	groups := ReferenceGroups("u")
	if len(groups) != 1 || len(groups[0].Items) != 2 {
		t.Fatalf("catalog = %+v", groups)
	}
	if got := atomic.LoadInt32(&src.listCalls); got != 1 {
		t.Errorf("ReferenceGroups called List %d times, want 1", got)
	}
}

func snapshotRefSources() map[string]ReferenceSource {
	refSourcesMu.RLock()
	defer refSourcesMu.RUnlock()
	out := make(map[string]ReferenceSource, len(refSources))
	for k, v := range refSources {
		out[k] = v
	}
	return out
}

func restoreRefSources(prev map[string]ReferenceSource) {
	refSourcesMu.Lock()
	refSources = prev
	refSourcesMu.Unlock()
}

func resetRefSources() {
	refSourcesMu.Lock()
	refSources = map[string]ReferenceSource{}
	refSourcesMu.Unlock()
}
