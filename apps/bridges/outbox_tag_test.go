package bridges

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The outbound name tag lets a recipient tell an agent's message apart from the
// owner's own texts. Three layers resolve at this transport chokepoint (most
// specific wins): the bound agent stamps its name onto OutboxItem.Agent only
// when it opted in; a GLOBAL override (bridgesConfig.TagOverride) can replace
// that name deployment-wide; and a PER-CHANNEL override can replace it again for
// one conversation, or disable the tag there entirely.
func TestEnqueueOutboxAgentNameTag(t *testing.T) {
	newBridges := func() *Bridges { return &Bridges{AppCore{DB: OpenCache()}} }

	readText := func(T *Bridges, id string) string {
		var it OutboxItem
		if !T.DB.Get(outboxTable, id, &it) {
			t.Fatalf("outbox item %q not stored", id)
		}
		return it.Text
	}

	// Channel lookups for per-channel overrides read the shared RootDB.
	saveRoot := RootDB
	RootDB = OpenCache()
	t.Cleanup(func() { RootDB = saveRoot })

	// Base case: agent stamped, no overrides → prefixed with the agent's name.
	t.Run("agent_name", func(t *testing.T) {
		T := newBridges()
		T.enqueueOutbox(OutboxItem{ID: "a1", Service: "imessage", Text: "hi", Agent: "Assistant", Type: "reply"})
		if got, want := readText(T, "a1"), "[Assistant] hi"; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// No agent stamped (didn't opt in) → untouched, never a bare "[] ".
	t.Run("no_agent_untagged", func(t *testing.T) {
		T := newBridges()
		T.enqueueOutbox(OutboxItem{ID: "a2", Service: "imessage", Text: "hi", Type: "reply"})
		if got := readText(T, "a2"); got != "hi" {
			t.Fatalf("got %q want %q", got, "hi")
		}
	})

	// Global override → replaces the agent's own name for every tagged message.
	t.Run("global_override", func(t *testing.T) {
		T := newBridges()
		T.setConfig(bridgesConfig{Enabled: true, TagOverride: "Brand"})
		T.enqueueOutbox(OutboxItem{ID: "a3", Service: "imessage", Text: "hi", Agent: "Assistant", Type: "reply"})
		if got, want := readText(T, "a3"), "[Brand] hi"; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// Per-channel override wins over both the agent name and the global override.
	t.Run("channel_override_wins", func(t *testing.T) {
		T := newBridges()
		T.setConfig(bridgesConfig{Enabled: true, TagOverride: "Brand"})
		SaveChannel(RootDB, Channel{ID: "c1", Owner: "u1", Service: "imessage", Address: "chat-1", AgentID: "ag1", TagOverride: "Support"})
		T.enqueueOutbox(OutboxItem{ID: "a4", Service: "imessage", ChatID: "chat-1", Owner: "u1", Text: "hi", Agent: "Assistant", Type: "reply"})
		if got, want := readText(T, "a4"), "[Support] hi"; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// Per-channel disable turns the tag off on that conversation even when the
	// agent opted in and a global override is set.
	t.Run("channel_disable", func(t *testing.T) {
		T := newBridges()
		T.setConfig(bridgesConfig{Enabled: true, TagOverride: "Brand"})
		SaveChannel(RootDB, Channel{ID: "c2", Owner: "u2", Service: "imessage", Address: "chat-2", AgentID: "ag1", TagDisabled: true})
		T.enqueueOutbox(OutboxItem{ID: "a5", Service: "imessage", ChatID: "chat-2", Owner: "u2", Text: "hi", Agent: "Assistant", Type: "reply"})
		if got := readText(T, "a5"); got != "hi" {
			t.Fatalf("got %q want %q", got, "hi")
		}
	})

	// The transient Owner is cleared before the item is stored, so it never
	// reaches a connector draining the outbox.
	t.Run("owner_not_persisted", func(t *testing.T) {
		T := newBridges()
		T.enqueueOutbox(OutboxItem{ID: "a6", Service: "imessage", ChatID: "chat-x", Owner: "u9", Text: "hi", Agent: "Assistant", Type: "reply"})
		var it OutboxItem
		T.DB.Get(outboxTable, "a6", &it)
		if it.Owner != "" {
			t.Fatalf("owner leaked into stored item: %q", it.Owner)
		}
	})
}
