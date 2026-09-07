package core

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCapturePromptTextIsTheTextNotTheSchemas: the capture exists to answer
// "what was actually in the prompt", which the digest's numbers cannot. The
// tool SCHEMAS are deliberately not in it — they are most of a modern prompt's
// bytes (153KB of one live 196KB turn) and the least informative part, since
// the digest counts them and the catalog log names every tool.
func TestCapturePromptTextIsTheTextNotTheSchemas(t *testing.T) {
	sys := "You are a helpful agent.\n[Style:] Do not use the word classic."
	tools := []AgentToolDef{{Tool: Tool{
		Name:        "web_search",
		Description: "SCHEMA-BODY-THAT-MUST-NOT-BE-CAPTURED",
	}}}
	msgs := []Message{
		{Role: "user", Content: "what is the weather"},
		{Role: "assistant", Content: "checking now", ToolCalls: []ToolCall{{Name: "web_search"}}},
	}

	got := capturePromptText(sys, tools, msgs)

	// The system prompt verbatim. This is the whole point: "was that rule even
	// in the prompt" was previously answered by grepping the source, which
	// reports what we INTEND to send, not what was sent.
	if !strings.Contains(got, "[Style:] Do not use the word classic.") {
		t.Errorf("the system prompt is not captured verbatim:\n%s", got)
	}
	// Tools by name, so the catalog is identifiable...
	if !strings.Contains(got, "web_search") {
		t.Error("the tool catalog is not named")
	}
	// ...but never their schemas.
	if strings.Contains(got, "SCHEMA-BODY-THAT-MUST-NOT-BE-CAPTURED") {
		t.Error("tool schemas are being captured — they are the bulk of the prompt and the digest already counts them")
	}
	// The conversation, with who said what and what it called.
	for _, want := range []string{"user", "what is the weather", "assistant", "checking now", "(tool call: web_search)"} {
		if !strings.Contains(got, want) {
			t.Errorf("capture is missing %q:\n%s", want, got)
		}
	}
}

// TestCapturedPromptNeverLandsInMetadata is the rule that makes the capture
// safe to have at all. ListRuns reads the metadata table to build the feed, so
// a prompt sitting there would surface the whole conversation in a listing.
// It belongs in the encrypted side table beside Raw, and only GetRun reads it.
func TestCapturedPromptNeverLandsInMetadata(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	rec := RecordRun(db, RunRecord{
		Owner:   "u",
		Agent:   "Chatty",
		Raw:     "the reply",
		Summary: "did a thing",
		Prompt:  PromptDigest{SystemBytes: 42, Text: "SENSITIVE-PROMPT-TEXT"},
	})

	var meta RunRecord
	if !db.Get(runLedgerTable, runLedgerKey("u", rec.ID), &meta) {
		t.Fatal("the run's metadata was not written")
	}
	if meta.Prompt.Text != "" {
		t.Error("the captured prompt is in the METADATA table, which the run feed reads")
	}
	if meta.Raw != "" {
		t.Error("Raw is in metadata — the rule the capture is following")
	}
	// The digest's numbers still live in metadata: they are always-on and the
	// feed is allowed to show them.
	if meta.Prompt.SystemBytes != 42 {
		t.Errorf("the digest numbers did not survive in metadata: %+v", meta.Prompt)
	}

	full, ok := GetRun(db, "u", rec.ID)
	if !ok {
		t.Fatal("the run could not be read back")
	}
	if full.Prompt.Text != "SENSITIVE-PROMPT-TEXT" {
		t.Errorf("GetRun did not rehydrate the captured prompt, got %q", full.Prompt.Text)
	}
	if full.Raw != "the reply" {
		t.Errorf("GetRun stopped rehydrating Raw: %q", full.Raw)
	}

	// A run recorded with capture OFF stores nothing, so the normal case costs
	// no storage and reads back clean.
	quiet := RecordRun(db, RunRecord{Owner: "u", Agent: "Chatty", Prompt: PromptDigest{SystemBytes: 7}})
	if got, _ := GetRun(db, "u", quiet.ID); got.Prompt.Text != "" {
		t.Errorf("a run with capture off came back with prompt text %q", got.Prompt.Text)
	}
}
