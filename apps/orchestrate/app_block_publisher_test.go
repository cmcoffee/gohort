package orchestrate

import (
	"strings"
	"testing"
)

// The publisher's contract is "safe to call, always" — an app builds its tools
// over one before there is any turn to bind, and the same tools run on
// headless dispatches that have no stream at all. Every one of those must be
// a quiet no-op rather than a panic in a tool handler goroutine.
func TestPublisherIsInertUntilBound(t *testing.T) {
	var nilPub *AppBlockPublisher
	nilPub.Publish(UIBlock{Type: "demo_card", ID: "1"}) // nil receiver

	unbound := &AppBlockPublisher{}
	unbound.Publish(UIBlock{Type: "demo_card", ID: "1"}) // never bound

	bound := &AppBlockPublisher{}
	bound.bind(&chatTurn{}) // bound to a turn with no sse and no session
	bound.Publish(UIBlock{Type: "demo_card", ID: "1"})

	// Reaching here without a panic IS the assertion.
}

// A block with no type has no renderer to dispatch to, so publishing one would
// put an event on the wire that the panel can only log a warning about.
func TestPublisherDropsTypelessBlocks(t *testing.T) {
	buf := &syncBuf{}
	pub := &AppBlockPublisher{}
	pub.bind(&chatTurn{sse: &sseWriter{live: buf}})

	pub.Publish(UIBlock{ID: "1", Title: "no type"})
	if buf.String() != "" {
		t.Fatalf("a typeless block was emitted: %s", buf.String())
	}
}

// The live half: what an app publishes has to arrive as a {kind:"block"}
// frame carrying the renderer's name and payload, because that is the shape
// the panel's block dispatcher matches on.
func TestPublishEmitsABlockFrame(t *testing.T) {
	buf := &syncBuf{}
	pub := &AppBlockPublisher{}
	pub.bind(&chatTurn{sse: &sseWriter{live: buf}})

	pub.Publish(UIBlock{
		Type:  "anvil_edit",
		ID:    "edit-1",
		Title: "main.go",
		Text:  "Edited · +3 −1",
		Data:  map[string]string{"diff": "@@ -1 +1 @@"},
	})

	out := buf.String()
	for _, want := range []string{
		`"kind":"block"`, `"type":"anvil_edit"`, `"id":"edit-1"`,
		"main.go", "Edited", "@@ -1 +1 @@",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("frame missing %q:\n%s", want, out)
		}
	}
}

// The durable half: a published block lands on the session so a reload
// replays it, and re-publishing the SAME id UPDATES that card rather than
// stacking a second one. Stacking is what makes a tool that revises its own
// card (a progress card, a diff that gets amended) unusable.
func TestPublishPersistsAndUpsertsByID(t *testing.T) {
	sess := &ChatSession{}
	pub := &AppBlockPublisher{}
	pub.bind(&chatTurn{session: sess})

	pub.Publish(UIBlock{Type: "demo_card", ID: "a", Title: "first"})
	pub.Publish(UIBlock{Type: "demo_card", ID: "b", Title: "other"})
	pub.Publish(UIBlock{Type: "demo_card", ID: "a", Title: "revised"})

	if len(sess.UIBlocks) != 2 {
		t.Fatalf("re-publishing an id stacked a card: %d blocks, want 2", len(sess.UIBlocks))
	}
	var found bool
	for _, b := range sess.UIBlocks {
		if b.ID == "a" {
			found = true
			if b.Title != "revised" {
				t.Fatalf("the upsert kept the stale title %q", b.Title)
			}
		}
	}
	if !found {
		t.Fatal("the re-published block is not on the session")
	}
}
