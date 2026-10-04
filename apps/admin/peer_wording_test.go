package admin

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Worker LLM section says whose prompt wording governs: nothing for a
// local worker, "none yet" for a peer that has not answered, and the peer,
// its model, the count and the age once it has, plus a note while the worker
// has just moved off it.
func TestTheWorkerSaysWhoseWordingGoverns(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { prompts.ClearPeerPromptLayer(); SetPromptOverrideDB(nil) })
	var key string
	for _, b := range AllPromptBlocks() {
		if b.Text != "" && !strings.Contains(b.Text, "{") {
			key = b.Key
			break
		}
	}
	if key == "" {
		t.Skip("no plain registered block")
	}

	if got := peerWordingLine("llama.cpp"); got != "" {
		t.Fatalf("local worker: %q", got)
	}
	if got := peerWordingLine("peer:den"); !strings.HasPrefix(got, "None from den yet.") {
		t.Fatalf("peer, nothing yet: %q", got)
	}
	prompts.SetPeerPromptLayer("den", "qwen3.6", time.Now(), map[string]string{key: "peer words"})
	if got := peerWordingLine("peer:den"); got != "1 block(s) of den's wording, tuned for qwen3.6, fetched just now." {
		t.Fatalf("peer with wording: %q", got)
	}
	if got := peerWordingLine("peer:attic"); !strings.Contains(got, "now attic's model") {
		t.Fatalf("worker moved to another peer: %q", got)
	}
	if got := peerWordingLine("llama.cpp"); !strings.Contains(got, "no longer a peer's model") {
		t.Fatalf("worker moved to a local model: %q", got)
	}
}
