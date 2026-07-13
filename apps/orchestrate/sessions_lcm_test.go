package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSessionDeleteWipesHistoryArchive: deleting a session (and, at the outer
// scope, an agent) must also drop the folded-conversation spans archived under
// lcm:<agent>:<session> — otherwise "clear thread" leaves the conversation's
// content recoverable via [history] recall and the archive grows forever.
func TestSessionDeleteWipesHistoryArchive(t *testing.T) {
	InvalidateChunkCache()
	t.Cleanup(InvalidateChunkCache)
	db := &DBase{Store: kvlite.MemStore()}

	seedSpan := func(id, sessID string) {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{
			ID: id, Source: operatorLCMSource("ag", sessID),
			ReportID: "span-" + id, Text: "archived turn",
		})
	}
	seedSpan("c1", "s1")
	seedSpan("c2", "s1")
	seedSpan("c3", "s2")
	// A chunk of another agent's archive must survive both wipes.
	db.Set(EmbeddedChunks, "other", EmbeddedChunk{
		ID: "other", Source: operatorLCMSource("other-agent", "s9"), Text: "not ours"})

	countLCM := func() map[string]int {
		out := map[string]int{}
		for _, k := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if db.Get(EmbeddedChunks, k, &c) {
				out[c.Source]++
			}
		}
		return out
	}

	deleteChatSession(db, "ag", "s1")
	got := countLCM()
	if got[operatorLCMSource("ag", "s1")] != 0 {
		t.Fatalf("session delete must wipe its archive: %v", got)
	}
	if got[operatorLCMSource("ag", "s2")] != 1 || got[operatorLCMSource("other-agent", "s9")] != 1 {
		t.Fatalf("session delete must not touch other sessions/agents: %v", got)
	}

	dropChatSessionBucket(db, "ag")
	got = countLCM()
	if got[operatorLCMSource("ag", "s2")] != 0 {
		t.Fatalf("agent delete must wipe every session's archive: %v", got)
	}
	if got[operatorLCMSource("other-agent", "s9")] != 1 {
		t.Fatalf("agent delete must not touch other agents' archives: %v", got)
	}
}
