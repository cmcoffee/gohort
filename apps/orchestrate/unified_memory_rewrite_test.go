package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// withUnifiedMemory installs an in-memory tunables DB with the unified-memory
// flag set to `on`, and returns a cleanup that restores the prior (nil) DB.
func withUnifiedMemory(t *testing.T, on bool) func() {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	v := float64(0)
	if on {
		v = 1
	}
	db.Set(WebTable, TunableUnifiedMemory, v)
	SetTunablesDB(db)
	return func() { SetTunablesDB(nil) }
}

// TestRewriteMemoryToolNamesLegacyNoop: under the legacy surface the rewrite must
// not touch a single character — the tuned prose ships exactly as written.
func TestRewriteMemoryToolNamesLegacyNoop(t *testing.T) {
	defer withUnifiedMemory(t, false)()
	in := "Before answering, call knowledge_search; capture gotchas via store_fact; drop stale notes with forget_fact."
	if got := rewriteMemoryToolNames(in); got != in {
		t.Fatalf("legacy mode must be a no-op\n in: %q\nout: %q", in, got)
	}
}

// TestRewriteMemoryToolNamesUnified: under the collapsed surface each legacy
// token is swapped for its unified equivalent, store_fact keeps its pin=true
// qualifier, and the distinct skill_* tools are never touched.
func TestRewriteMemoryToolNamesUnified(t *testing.T) {
	defer withUnifiedMemory(t, true)()

	cases := []struct{ in, want string }{
		{"call knowledge_search first", "call recall first"},
		{"pull the full doc via fetch_knowledge_doc", "pull the full doc via recall"},
		{"search_facts / recall_history / expand_history / list_facts", "recall / recall / recall / recall"},
		{"capture gotchas via store_fact", "capture gotchas via remember (pin=true)"},
		{"stash a finding via memory_save", "stash a finding via remember"},
		{"drop it with forget_fact or memory_forget", "drop it with forget or forget"},
	}
	for _, c := range cases {
		if got := rewriteMemoryToolNames(c.in); got != c.want {
			t.Errorf("rewrite\n in: %q\ngot: %q\nwant: %q", c.in, got, c.want)
		}
	}

	// Collision safety: skill_knowledge_search and skill_knowledge_fetch_doc are
	// DIFFERENT tools that exist in both modes. The word-boundary regex must not
	// touch them (the underscore before "knowledge" blocks the boundary).
	skills := "read_skill / skill_knowledge_search / skill_knowledge_fetch_doc"
	if got := rewriteMemoryToolNames(skills); got != skills {
		t.Fatalf("skill_* tools must be untouched\n in: %q\nout: %q", skills, got)
	}
	if strings.Contains(rewriteMemoryToolNames(skills), "skill_recall") {
		t.Fatal("rewrite corrupted a skill_ tool name")
	}
}

// TestRewriteMemoryToolNamesIdempotent: the two call sites (prependAgentContext
// and appendAgentCapabilityBlocks) may both run over the same string, so a
// second pass must be a no-op — the replacements carry no legacy token.
func TestRewriteMemoryToolNamesIdempotent(t *testing.T) {
	defer withUnifiedMemory(t, true)()
	in := "call knowledge_search then store_fact the result"
	once := rewriteMemoryToolNames(in)
	twice := rewriteMemoryToolNames(once)
	if once != twice {
		t.Fatalf("rewrite not idempotent\nonce:  %q\ntwice: %q", once, twice)
	}
}
