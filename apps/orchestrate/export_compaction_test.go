package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A long thread is TRIMMED in storage: leading messages already folded into the
// summary and archived to recall are dropped so it cannot grow without bound.
// The export could only ever show what is stored — and it printed the session's
// original Created timestamp above a transcript that began hours later, with no
// mention of the gap. Read by a human, that is not a retention policy, it is a
// copy that failed. Reported as exactly that: "I don't think the full session
// is copying."
func TestExportSaysWhenTheThreadWasCompacted(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "ag", Name: "Builder"}
	sess := ChatSession{
		ID: "s1", Title: "long one",
		Created: time.Now().Add(-4 * time.Hour), LastAt: time.Now(),
		Messages: []ChatMessage{{Role: "user", Content: "the surviving tail"}},
	}
	saveCompactState(db, agent.ID, sess.ID, CompactState{
		Summary: "Earlier: built two agents and a pipeline.", SummarizedThrough: 40, FoldSeq: 2,
	})

	md := renderSessionMarkdownWithDiag(agent, sess, db)
	if !strings.Contains(md, "not reproduced verbatim") {
		t.Error("the export must say the earlier turns are missing, or it reads as a broken copy")
	}
	if !strings.Contains(md, "Earlier: built two agents and a pipeline.") {
		t.Error("carry the summary — it is what stands in for the span that was dropped")
	}
	if !strings.Contains(md, "2 times") {
		t.Errorf("say how much is behind the summary; got no fold count:\n%s", md)
	}
	if !strings.Contains(md, "the surviving tail") {
		t.Error("the verbatim tail still has to be there")
	}

	// JSON calls itself the lossless shape; a truncation it does not mention is
	// the one way it could lie.
	var payload map[string]any
	b, err := json.Marshal(buildExportPayload(agent, sess, db))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	sessOut, _ := payload["session"].(map[string]any)
	comp, ok := sessOut["compacted"].(map[string]any)
	if !ok {
		t.Fatalf("the JSON export must record that messages is a TAIL, got %v", sessOut)
	}
	if comp["summary"] == "" || comp["folds"] != float64(2) {
		t.Errorf("compaction block is incomplete: %v", comp)
	}
}

// A short session is untouched — no note, no block, nothing to explain. A
// disclaimer on every export is a disclaimer nobody reads.
func TestExportIsUnchangedForAnUncompactedSession(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "ag", Name: "Builder"}
	sess := ChatSession{ID: "s2", Title: "short", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	if md := renderSessionMarkdownWithDiag(agent, sess, db); strings.Contains(md, "not reproduced verbatim") {
		t.Error("nothing was folded; the export must not imply anything is missing")
	}
	if got := buildExportPayload(agent, sess, db); got.Session.Compacted != nil {
		t.Errorf("no fold state means no compaction block, got %+v", got.Session.Compacted)
	}
	// And a caller with no store at all still renders.
	if md := renderSessionMarkdown(agent, sess); !strings.Contains(md, "hi") {
		t.Error("a nil store must not break the export")
	}
}
