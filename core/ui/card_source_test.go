package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// A card is where a page puts what it worked out on the server, and that
// is exactly the content a save on the same page invalidates. Table,
// DisplayPanel and ChartPanel have refetched on invalidation for a
// while; every server-rendered BLOCK had to hand-roll the same fetch,
// debounce and swap in its own app — twice so far, in two editors.
func TestACardWithASourceStaysCurrent(t *testing.T) {
	misc := mustRuntimePart(t, "70_misc.js")
	card := misc[strings.Index(misc, "components.card ="):]
	card = card[:strings.Index(card, "components.frame =")]

	for _, want := range []struct{ src, why string }{
		{"ui-data-changed", "a card that cannot hear an invalidation is the problem this solves"},
		{"uiAutoRefresh(cfg.auto_refresh_ms", "polling goes through the shared poller, not a bare setInterval"},
		{"setTimeout(reload, 250)", "a panel saves per field group; three saves are not three fetches"},
		{"ui-card-refreshed", "the content is new elements — a page decorating them has to be told"},
		{"indexOf(w) === 0", "the write that changed the block carries its record in the query, so match by prefix"},
	} {
		if !strings.Contains(card, want.src) {
			t.Errorf("the card renderer lost %q: %s", want.src, want.why)
		}
	}
	// A failed refresh must leave the last good paint standing rather
	// than blanking the block.
	if !strings.Contains(card, "if (body === null) return;") {
		t.Error("a failed refresh should keep what is on screen")
	}
	// The Go side has to offer the fields, or none of it is reachable.
	src := mustFile(t, "components.go")
	decl := src[strings.Index(src, "type Card struct"):]
	decl = decl[:strings.Index(decl, "\n}\n")]
	for _, f := range []string{"Source string", "RefreshOn []string", "AutoRefreshMS int"} {
		if !strings.Contains(decl, f) {
			t.Errorf("Card is missing %s; the runtime support is unreachable from Go", f)
		}
	}
}

// The server still renders the block into the page. A sourced card that
// mounted empty and then fetched would flash "Loading…" on every arrival
// for content the page already had.
func TestASourcedCardKeepsItsServerRenderedFirstPaint(t *testing.T) {
	b, err := json.Marshal(Card{HTML: "<p>drawn</p>", Source: "api/thing/graph",
		RefreshOn: []string{"api/thing/parts"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"html":"\u003cp\u003edrawn\u003c/p\u003e"`,
		`"source":"api/thing/graph"`, `"refresh_on":["api/thing/parts"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("a sourced card should carry %s:\n%s", want, b)
		}
	}
	// And a plain card stays plain — every page that has one renders it
	// unchanged, with nothing extra on the wire.
	plain, _ := json.Marshal(Card{HTML: "<p>hi</p>"})
	if strings.Contains(string(plain), "source") || strings.Contains(string(plain), "refresh_on") {
		t.Errorf("a card with no source should not ship the fields:\n%s", plain)
	}

	misc := mustRuntimePart(t, "70_misc.js")
	card := misc[strings.Index(misc, "components.card ="):]
	card = card[:strings.Index(card, "components.frame =")]
	if !strings.Contains(card, "paint(cfg.html)") {
		t.Error("the server-rendered HTML should be the first paint")
	}
	if !strings.Contains(card, "if (!cfg.html) reload();") {
		t.Error("a card with only a source has nothing to show until it fetches")
	}
	if !strings.Contains(card, "if (!cfg.source) return wrap;") {
		t.Error("a plain card must not fetch, listen or poll")
	}
}
