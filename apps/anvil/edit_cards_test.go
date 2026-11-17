package anvil

import (
	"encoding/json"
	"strings"
	"testing"
)

// A turn's writes to one file share a card: each later write sends the same
// card again with the turn's whole change to the file as its diff and every
// write behind it, so its Revert and a message's Undo reach all of them. A
// write to another file is a card of its own, and a single write is the card
// it always was.
func TestATurnsWritesToOneFileShareACard(t *testing.T) {
	p := Project{ID: "p1"}
	var cards fileCards
	write := func(id, path, kind, before, after string) EditEntry {
		d, a, r := unifiedDiff(path, before, after)
		return EditEntry{ID: id, Path: path, Kind: kind, Before: before, After: after, Diff: d, Added: a, Removed: r}
	}
	one := cards.add("anvil_edit", p, write("e1", "main.go", EditModify, "a\nb\n", "a\nB\n"))
	if one.ID != "anvil-edit-e1" || one.Data["edit_id"] != "e1" || strings.Contains(one.Text, "edits") {
		t.Fatalf("a single write: %+v", one)
	}
	other := cards.add("anvil_edit", p, write("e2", "util.go", EditCreate, "", "x\n"))
	if other.ID == one.ID {
		t.Fatal("a write to another file joined the first file's card")
	}
	again := cards.add("anvil_edit", p, write("e3", "main.go", EditModify, "a\nB\n", "a\nB\nc\n"))
	if again.ID != one.ID {
		t.Fatalf("the second write to main.go made a new card: %s", again.ID)
	}
	if again.Data["edit_id"] != "e1,e3" || !strings.Contains(again.Text, "2 edits") {
		t.Errorf("the card does not hold both writes: %q %q", again.Data["edit_id"], again.Text)
	}
	if d := again.Data["diff"]; !strings.Contains(d, "-b") || !strings.Contains(d, "+B") || !strings.Contains(d, "+c") {
		t.Errorf("the card's diff is not the turn's whole change to the file:\n%s", d)
	}
	var each []map[string]any
	if json.Unmarshal([]byte(again.Data["edits"]), &each) != nil || len(each) != 2 || each[1]["edit_id"] != "e3" {
		t.Errorf("each write is not behind the card: %s", again.Data["edits"])
	}
	if !strings.Contains(anvilEditBlockJS, "onUpdate: fill") || !strings.Contains(anvilEditBlockJS, "api/undo") {
		t.Error("the card does not fill in place, or does not revert through the undo")
	}
}
