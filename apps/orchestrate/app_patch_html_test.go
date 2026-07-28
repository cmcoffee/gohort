package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func gameSections() []map[string]any {
	return []map[string]any{{
		"kind":  "html",
		"title": "Law Bird",
		"html": `<!DOCTYPE html><html><head><style>*{margin:0}</style></head><body>
<canvas id="g"></canvas>
<script>
var GAP = 155;
var bird = {x: 80, y: 300, w: 28, h: 28};
function spawn() { pipes.push({x: 400, gap: GAP}); }
function draw() { ctx.fillRect(bird.x, bird.y, bird.w, bird.h); }
</script></body></html>`,
	}}
}

// TestApplyHTMLPatchReplacesOnlyTheMatch — everything outside the match must
// come through byte-for-byte. That is the entire reason to patch rather than
// re-send: a re-typed document is where the unrequested rewrites came from.
func TestApplyHTMLPatchReplacesOnlyTheMatch(t *testing.T) {
	secs := gameSections()
	before := mapStr(secs[0], "html")
	got, err := applyHTMLPatch(secs, 0, "var GAP = 155;", "var GAP = 170;", "law-bird")
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if !strings.Contains(got, "var GAP = 170;") {
		t.Error("replacement did not land")
	}
	if strings.Contains(got, "var GAP = 155;") {
		t.Error("original text survived")
	}
	// Byte-for-byte outside the match.
	wantRest := strings.Replace(before, "var GAP = 155;", "var GAP = 170;", 1)
	if got != wantRest {
		t.Error("patch changed something outside the matched region")
	}
	if len(got) != len(before)+0 {
		t.Errorf("length drift: %d → %d (same-length replacement)", len(before), len(got))
	}
}

// TestApplyHTMLPatchRefusesZeroMatches — patching text the app doesn't have
// means the author is working from a stale copy. Point it at get rather than
// silently doing nothing.
func TestApplyHTMLPatchRefusesZeroMatches(t *testing.T) {
	secs := gameSections()
	_, err := applyHTMLPatch(secs, 0, "var GAP = 999;", "var GAP = 170;", "law-bird")
	if err == nil {
		t.Fatal("expected a refusal for a find that matches nothing")
	}
	if !strings.Contains(err.Error(), "action=\"get\"") {
		t.Errorf("error should point at reading the current html: %v", err)
	}
}

// TestApplyHTMLPatchRefusesMultipleMatches — with several matches the patch
// cannot report which place it changed, so it must not choose one.
func TestApplyHTMLPatchRefusesMultipleMatches(t *testing.T) {
	secs := gameSections()
	_, err := applyHTMLPatch(secs, 0, "bird.", "lawyer.", "law-bird")
	if err == nil {
		t.Fatal("expected a refusal for an ambiguous find")
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("error should ask for more context: %v", err)
	}
	// And nothing was changed.
	if !strings.Contains(mapStr(secs[0], "html"), "bird.x") {
		t.Error("a refused patch must leave the section untouched")
	}
}

// TestApplyHTMLPatchDeletes — an empty replacement removes the matched text.
func TestApplyHTMLPatchDeletes(t *testing.T) {
	secs := gameSections()
	got, err := applyHTMLPatch(secs, 0, "\nfunction spawn() { pipes.push({x: 400, gap: GAP}); }", "", "law-bird")
	if err != nil {
		t.Fatalf("delete patch failed: %v", err)
	}
	if strings.Contains(got, "function spawn()") {
		t.Error("text was not deleted")
	}
	if !strings.Contains(got, "function draw()") {
		t.Error("delete took out more than the match")
	}
}

func TestPickHTMLSection(t *testing.T) {
	one := gameSections()
	if idx, err := pickHTMLSection(one, nil); err != nil || idx != 0 {
		t.Fatalf("single html section should need no `section` arg: idx=%d err=%v", idx, err)
	}

	// A table in front of the html section: the returned index is the position
	// in the SECTIONS array, while `section` counts html sections only.
	mixed := []map[string]any{
		{"kind": "table", "columns": []any{}},
		{"kind": "html", "html": "<p>a</p>"},
	}
	idx, err := pickHTMLSection(mixed, nil)
	if err != nil || idx != 1 {
		t.Fatalf("html section behind a table: idx=%d err=%v", idx, err)
	}
	if ord := htmlSectionOrdinal(mixed, idx); ord != 1 {
		t.Errorf("ordinal among html sections = %d, want 1", ord)
	}

	two := []map[string]any{
		{"kind": "html", "html": "<p>first</p>"},
		{"kind": "table", "columns": []any{}},
		{"kind": "html", "html": "<p>second</p>"},
	}
	if _, err := pickHTMLSection(two, nil); err == nil {
		t.Error("two html sections with no `section` arg must refuse rather than guess")
	}
	idx, err = pickHTMLSection(two, float64(2))
	if err != nil || mapStr(two[idx], "html") != "<p>second</p>" {
		t.Fatalf("section=2 should select the SECOND html section: idx=%d err=%v", idx, err)
	}
	if ord := htmlSectionOrdinal(two, idx); ord != 2 {
		t.Errorf("ordinal = %d, want 2", ord)
	}
	if _, err := pickHTMLSection(two, float64(3)); err == nil {
		t.Error("out-of-range section must error")
	}
	if _, err := pickHTMLSection([]map[string]any{{"kind": "table"}}, nil); err == nil {
		t.Error("an app with no html section must say so")
	}
}

// TestAppAuthoringSectionsFallsBackToPage — an app saved before sections were
// stored is still patchable, because an html section reverses out of the
// rendered page losslessly.
func TestAppAuthoringSectionsFallsBackToPage(t *testing.T) {
	spec := AppSpec{Slug: "game", Name: "Game", RecordKey: "id"}
	page, err := buildAppPage(spec, []any{
		map[string]any{"kind": "html", "title": "Play", "html": "<!doctype html><html><body><script>var a = 1;</script></body></html>"},
	})
	if err != nil {
		t.Fatalf("buildAppPage: %v", err)
	}
	spec.Page, _ = page.ConfigJSON()

	secs, err := appAuthoringSections(spec) // no spec.Sections at all
	if err != nil {
		t.Fatalf("legacy spec should still be patchable: %v", err)
	}
	if len(secs) != 1 || !strings.Contains(mapStr(secs[0], "html"), "var a = 1;") {
		t.Fatalf("reversed sections = %+v", secs)
	}

	// And the stored authoring copy wins when present.
	spec.Sections, _ = json.Marshal([]map[string]any{{"kind": "html", "html": "<p>stored</p>"}})
	secs, err = appAuthoringSections(spec)
	if err != nil || mapStr(secs[0], "html") != "<p>stored</p>" {
		t.Fatalf("stored sections should win: %+v err=%v", secs, err)
	}
}

// TestPatchedSectionsStillBuild — the patched array has to go back through the
// normal page builder, or a patch could produce a spec that no longer renders.
func TestPatchedSectionsStillBuild(t *testing.T) {
	spec := AppSpec{Slug: "law-bird", Name: "Law Bird", RecordKey: "id"}
	secs := gameSections()
	patched, err := applyHTMLPatch(secs, 0, "var GAP = 155;", "var GAP = 170;", spec.Slug)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	secs[0]["html"] = patched

	raw := make([]any, len(secs))
	for i := range secs {
		raw[i] = secs[i]
	}
	page, err := buildAppPage(spec, raw)
	if err != nil {
		t.Fatalf("patched sections must still build: %v", err)
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	if !strings.Contains(string(blob), "var GAP = 170;") {
		t.Error("the patch did not reach the rendered page")
	}
	// A full document still routes to its own frame after patching.
	if !strings.Contains(string(blob), `"type":"frame"`) {
		t.Error("patched full document should still render in a frame")
	}
}
