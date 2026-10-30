package agenthost

import (
	"context"
	"strings"
	"testing"
)

// propose_edit puts the passage and its replacement in the conversation as a
// text_proposal card, the shape the client's review reads; it refuses a call
// that would change nothing.
func TestProposeEditPublishesAPassageAndItsReplacement(t *testing.T) {
	var got []Block
	pub := &Publisher{}
	pub.Bind(func(b Block) { got = append(got, b) })
	tool := ProposeEditTool(pub)
	if tool.Tool.Name != "propose_edit" {
		t.Fatalf("name %q", tool.Tool.Name)
	}
	out, err := tool.Handler(context.Background(), map[string]any{
		"find": "Neon on the water\n", "replace": "Neon on the river\n", "summary": " Water to river ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("one card: %d", len(got))
	}
	b := got[0]
	if b.Type != "text_proposal" || b.ID == "" || b.Text != "Water to river" ||
		b.Data["find"] != "Neon on the water\n" || b.Data["replace"] != "Neon on the river\n" {
		t.Errorf("the card carries the passage, its replacement and the summary: %+v", b)
	}
	if !strings.Contains(out, "Do not repeat") {
		t.Errorf("the model is told not to paste the text again: %q", out)
	}

	// A whole-document proposal, and a deletion, are both changes.
	if _, err := tool.Handler(context.Background(), map[string]any{"replace": "all new", "summary": "rewrite"}); err != nil {
		t.Errorf("an empty find proposes the whole document: %v", err)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"find": "cut me", "replace": "", "summary": "cut"}); err != nil {
		t.Errorf("an empty replace removes the passage: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"nothing at all": {"summary": "x"},
		"the same text":  {"find": "same", "replace": "same", "summary": "x"},
	} {
		n := len(got)
		if _, err := tool.Handler(context.Background(), args); err == nil || len(got) != n {
			t.Errorf("%s: refused and nothing published (err=%v)", name, err)
		}
	}
	// An unbound publisher is inert, not a crash.
	if _, err := ProposeEditTool(&Publisher{}).Handler(context.Background(), map[string]any{"replace": "x", "summary": "y"}); err != nil {
		t.Errorf("unbound: %v", err)
	}
}

// The turn is told which passage the message is about, fenced so a passage
// holding a code fence cannot end it early, and nothing when none is pinned.
func TestSelectionContext(t *testing.T) {
	if SelectionContext(Selection{}) != "" || SelectionContext(Selection{Text: "  \n"}) != "" {
		t.Error("nothing pinned says nothing")
	}
	got := SelectionContext(Selection{Text: "Verse\n```\ncode\n```", Start: 4, End: 20})
	if !strings.Contains(got, "Verse\n```\ncode\n```") || !strings.Contains(got, "````") {
		t.Errorf("the passage, under a fence longer than any inside it: %q", got)
	}
	if !strings.Contains(got, "propose_edit") || !strings.Contains(got, "do not propose the whole document") {
		t.Errorf("and how to propose a change to it: %q", got)
	}
	if strings.Contains(got, "—") {
		t.Error("no em-dash in prompt text")
	}
}

// A highlighted passage in a document its agent changes its own way: the
// turn is told the passage and to keep a change to it, not to propose one.
func TestHighlightContext(t *testing.T) {
	if HighlightContext(Selection{Text: "  "}) != "" {
		t.Error("nothing highlighted says nothing")
	}
	got := HighlightContext(Selection{Text: "Restart the service first."})
	if !strings.Contains(got, "Restart the service first.") || !strings.Contains(got, "change that passage") {
		t.Errorf("the turn reads:\n%s", got)
	}
	if strings.Contains(got, "propose_edit") || strings.Contains(got, "—") {
		t.Errorf("no propose_edit, no em-dash:\n%s", got)
	}
}
