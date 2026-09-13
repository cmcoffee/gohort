package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ids are derived from kind and title, made unique, and never overwrite one
// the author supplied; the input array is left untouched.
func TestEnsureSectionIDsIsStableAndUnique(t *testing.T) {
	in := []any{
		map[string]any{"kind": "form", "title": "New entry"},
		map[string]any{"kind": "table"},
		map[string]any{"kind": "table"},
		map[string]any{"kind": "html", "id": "game", "html": "<canvas></canvas>"},
		map[string]any{"columns": []any{map[string]any{"field": "x"}}}, // kind implied
	}
	out, _ := ensureSectionIDs(in).([]any)
	got := make([]string, len(out))
	for i, item := range out {
		got[i] = mapStr(item.(map[string]any), "id")
	}
	if strings.Join(got, ",") != "form-new-entry,table,table-2,game,table-3" {
		t.Fatalf("ids = %v", got)
	}
	if _, has := in[0].(map[string]any)["id"]; has {
		t.Fatal("input was mutated")
	}
	// A second pass keeps every id.
	again, _ := ensureSectionIDs(out).([]any)
	for i := range again {
		if mapStr(again[i].(map[string]any), "id") != got[i] {
			t.Fatalf("id %d changed on re-run", i)
		}
	}
	// The stored-blob form does the same for a spec saved before ids existed.
	blob, _ := json.Marshal(in)
	var back []map[string]any
	if err := json.Unmarshal(ensureSectionIDsJSON(blob), &back); err != nil || back[0]["id"] != "form-new-entry" {
		t.Fatalf("json form: %v %v", err, back)
	}
}

// A patch target may be a section id; a non-html id is refused by kind and an
// unknown id names the problem.
func TestPickHTMLSectionAcceptsIDs(t *testing.T) {
	secs := appProposedSections(ensureSectionIDs([]any{
		map[string]any{"kind": "table", "title": "Rows"},
		map[string]any{"kind": "html", "title": "Game", "html": "<b>a</b>"},
		map[string]any{"kind": "html", "title": "Help", "html": "<b>b</b>"},
	}))
	if i, err := pickHTMLSection(secs, "html-help"); err != nil || i != 2 {
		t.Fatalf("by id: %d %v", i, err)
	}
	if i, err := pickHTMLSection(secs, "2"); err != nil || i != 2 {
		t.Fatalf("by ordinal string: %d %v", i, err)
	}
	if _, err := pickHTMLSection(secs, "table-rows"); err == nil || !strings.Contains(err.Error(), "not html") {
		t.Fatalf("non-html id should refuse by kind: %v", err)
	}
	if _, err := pickHTMLSection(secs, "nope"); err == nil || !strings.Contains(err.Error(), "no section with id") {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestFindSectionByIDAndList(t *testing.T) {
	secs := appProposedSections(ensureSectionIDs([]any{
		map[string]any{"kind": "form", "title": "Entry"},
		map[string]any{"kind": "table"},
	}))
	if findSectionByID(secs, "table") != 1 || findSectionByID(secs, "") != -1 || findSectionByID(secs, "x") != -1 {
		t.Fatal("lookup wrong")
	}
	if l := sectionIDList(secs); l != "form-entry (form), table (table)" {
		t.Fatalf("list = %q", l)
	}
	if sectionIDList(nil) == "" {
		t.Fatal("empty list should still say something")
	}
}
