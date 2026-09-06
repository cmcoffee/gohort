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

// fakeRefSource is a plain ReferenceSource — no tool provider — so
// ReferenceItemTools* has to synthesize the default search tool for it.
type fakeRefSource struct {
	kind    string
	sawDone bool // whether the ctx handed to Fetch was already canceled
}

func (f *fakeRefSource) Kind() string  { return f.kind }
func (f *fakeRefSource) Label() string { return "Fakes" }
func (f *fakeRefSource) List(user string) []ReferenceItem {
	return []ReferenceItem{{ID: "item1", Name: "Widget Store"}}
}
func (f *fakeRefSource) Fetch(ctx context.Context, user, itemID, query string) string {
	f.sawDone = ctx.Err() != nil
	return "some text"
}

// The synthesized search tool must run on the TURN's context. Rooted on
// context.Background() a remote source keeps being waited on after the
// user stops the turn.
func TestSynthesizedSearchToolUsesTheSessionContext(t *testing.T) {
	src := &fakeRefSource{kind: "faketestsrc"}
	RegisterReferenceSource(src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defs := ReferenceItemToolsWithSession(&ToolSession{Ctx: ctx}, "u", "faketestsrc", "item1")
	if len(defs) != 1 {
		t.Fatalf("want the default search tool, got %d defs", len(defs))
	}
	if _, err := defs[0].Handler(map[string]any{"query": "anything"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !src.sawDone {
		t.Error("Fetch was handed a live context — a stopped turn cannot stop this source")
	}
}

// No session is legal: apps that never wired one keep the old behavior
// rather than panicking on a nil deref.
func TestNilSessionFallsBackToBackground(t *testing.T) {
	src := &fakeRefSource{kind: "faketestsrc2"}
	RegisterReferenceSource(src)

	defs := ReferenceItemTools("u", "faketestsrc2", "item1")
	if len(defs) != 1 {
		t.Fatalf("want the default search tool, got %d defs", len(defs))
	}
	if _, err := defs[0].Handler(map[string]any{"query": "anything"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if src.sawDone {
		t.Error("a nil session must give a live context, not a canceled one")
	}
}

// bothProviderSource implements BOTH shapes. The session-aware one has to
// win, or a source keeps its detached tools the moment it grows the plain
// method for a legacy caller.
type bothProviderSource struct{ fakeRefSource }

func (b *bothProviderSource) ItemTools(user, itemID string) []AgentToolDef {
	return []AgentToolDef{{Tool: Tool{Name: "plain_form"}}}
}
func (b *bothProviderSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	return []AgentToolDef{{Tool: Tool{Name: "session_form"}}}
}

func TestSessionAwareProviderWinsOverThePlainOne(t *testing.T) {
	RegisterReferenceSource(&bothProviderSource{fakeRefSource{kind: "faketestsrc3"}})

	defs := ReferenceItemToolsWithSession(&ToolSession{}, "u", "faketestsrc3", "item1")
	if len(defs) != 1 || defs[0].Tool.Name != "session_form" {
		t.Fatalf("want the session-aware provider to win, got %+v", defs)
	}
	// And the legacy entry point still resolves through it, with a nil
	// session — one code path, not two.
	if defs := ReferenceItemTools("u", "faketestsrc3", "item1"); len(defs) != 1 || defs[0].Tool.Name != "session_form" {
		t.Fatalf("legacy entry point should route through the same provider, got %+v", defs)
	}
}
