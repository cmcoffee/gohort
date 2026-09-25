package orchestrate

// The dead-tool rule is a REGISTRY LOOKUP now, not a reading of the sentence
// around a name. These pin the property that bought: a name that was never a
// tool is never reported, however much it looks like a call.

import (
	"strings"
	"testing"
)

func none() map[string]bool { return map[string]bool{} }

// The failure that drove this. Reference Memory holds code; snake_case followed
// by "(" is what a function call looks like, and every heuristic that tried to
// tell the two apart got this wrong.
func TestStoredCodeIsNeverReadAsToolMentions(t *testing.T) {
	for _, text := range []string{
		"```python\ndef run():\n    cfg = parse_config(path)\n    return write_output(cfg)\n```",
		"    cfg = parse_config(path)\n    rows = fetch_rows(cfg)\n    return write_output(rows)\n",
		"the handler calls parse_config to load settings, then hands off",
		"We used to call parse_config(path) here.",
	} {
		if got := deadToolFindings("Reference Memory", text, none(), none()); len(got) != 0 {
			t.Errorf("a name that was never a tool must never be reported\n text: %q\n got: %+v", text, got)
		}
	}
}

// A retired name IS reported, wherever it appears — no verb required, because
// the registry is the evidence rather than the sentence.
func TestARetiredToolIsReported(t *testing.T) {
	retired := map[string]bool{"store_fact": true}
	for _, text := range []string{
		"capture gotchas via store_fact",
		"store_fact is how we keep the pinned notes",
		"    result = store_fact(note)\n",
	} {
		got := deadToolFindings("Saved facts", text, none(), retired)
		if len(got) != 1 {
			t.Fatalf("want one finding for %q, got %+v", text, got)
		}
		if !strings.Contains(got[0].Detail, "store_fact") {
			t.Errorf("should name the tool: %s", got[0].Detail)
		}
	}
}

// An orphaned tool keeps its own finding: it still exists as a record, it just
// has no carrier, and the fix is different (re-home it, not rewrite the note).
func TestAnOrphanedToolKeepsItsOwnMessage(t *testing.T) {
	got := deadToolFindings("Reference Memory", "we still reference ts3_list_clients here",
		map[string]bool{"ts3_list_clients": true}, none())
	if len(got) != 1 {
		t.Fatalf("want one finding, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "Orphaned Tools") {
		t.Errorf("an orphan should point at the orphan pool: %s", got[0].Detail)
	}
}

// Word-bounded, or retiring a short name reports every longer one containing it.
func TestRetiredNamesMatchOnWordBoundaries(t *testing.T) {
	retired := map[string]bool{"memory": true, "store_fact": true}
	for _, text := range []string{
		"memory_save is the new way",    // longer name, must not match "memory"
		"the store_factory builds them", // must not match "store_fact"
		"read the memory_search docs",
	} {
		if got := deadToolFindings("Working notes", text, none(), retired); len(got) != 0 {
			t.Errorf("%q should not match a shorter retired name: %+v", text, got)
		}
	}
	// The bare name still matches when it is written as the tool. As a plain
	// word ("check memory first") it is English, not a call, and flagging it
	// is the false positive that repeated on every open of the pane.
	if got := deadToolFindings("Working notes", "check memory() first", none(), retired); len(got) != 1 {
		t.Errorf("a call to the bare name should match: %+v", got)
	}
	if got := deadToolFindings("Working notes", "check memory first", none(), retired); len(got) != 0 {
		t.Errorf("the plain word is not the tool: %+v", got)
	}
}

// Findings land in a rendered list, so map iteration must not reshuffle them
// between two opens of the same pane.
func TestFindingOrderIsStable(t *testing.T) {
	retired := map[string]bool{"store_fact": true, "knowledge_search": true, "recall_history": true}
	text := "we used store_fact, knowledge_search and recall_history"
	first := deadToolFindings("Saved facts", text, none(), retired)
	for i := 0; i < 20; i++ {
		got := deadToolFindings("Saved facts", text, none(), retired)
		if len(got) != len(first) {
			t.Fatalf("count changed: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j].Detail != first[j].Detail {
				t.Fatalf("order changed between runs at %d", j)
			}
		}
	}
}
