package ui

// What a Table does with a payload that is not the list it expected.
//
// It used to take the first key's value whatever it was, so a single
// record, an error body, or the HTML of a 404 page all became `records`
// — and the next line called .filter on it. "records.filter is not a
// function" names the symptom and nothing else; the fault that produced
// it was several layers away, in a URL that resolved against the wrong
// base.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestATableSurvivesAPayloadThatIsNotAList(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "10_basics.js")
	at := strings.Index(src, "function firstRecordList(d) {")
	if at < 0 {
		t.Fatal("the record extractor moved")
	}
	end := strings.Index(src[at:], "\n    }\n")
	if end < 0 {
		t.Fatal("could not bound the function")
	}
	fn := src[at : at+end+7]

	harness := fn + `
function eq(got, want, why) {
  var g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) throw new Error(why + ': got ' + g + ' want ' + w);
}
// The ordinary shapes.
eq(firstRecordList([{a:1}]), [{a:1}], 'a bare array is the list');
eq(firstRecordList({conversations:[1,2]}), [1,2], 'the conventional key wins');
eq(firstRecordList({pipelines:[3]}), [3], 'a shaped object gives up its list');
// A shaped object whose FIRST key is not the list — this is what a
// count-then-items payload looks like, and taking key order alone
// returned the count.
eq(firstRecordList({total: 2, items: [1,2]}), [1,2], 'the first ARRAY value, not the first value');
// The three that used to crash.
eq(firstRecordList('<!doctype html><html>404'), [], 'html from a wrong-base fetch is not a list');
eq(firstRecordList({name:'cred', type:'bearer'}), [], 'a single record is not a list');
eq(firstRecordList({error:'no such credential'}), [], 'an error body is not a list');
eq(firstRecordList(null), [], 'nothing is not a list');
eq(firstRecordList(7), [], 'a number is not a list');
// And whatever comes back, it can be filtered — which is the line that
// used to throw.
['x', {}, null, [1], {a:{}}].forEach(function(v){ firstRecordList(v).filter(function(){ return true; }); });
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "records.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the record extractor does not hold:\n%s", out)
	}
}

// A Table row is the surface behind every framework list, including the
// live-activity ones, and it is the same flex line that was crushing the
// hand-rolled live rows elsewhere: fixed-width neighbours, one long text
// field, no room. It does not crush, and these are the three rules that
// are the reason. A Col.Flex lands as an inline `flex: N` (grow N, basis 0),
// so without min-width:0 a cell would refuse to shrink past its longest word
// and shove the row wider than the card holding it.
func TestTableCellsTruncateRatherThanCrush(t *testing.T) {
	cell := cssRule(t, ".ui-table-cell")
	for _, want := range []string{"min-width: 0", "white-space: nowrap", "text-overflow: ellipsis"} {
		if !strings.Contains(cell, want) {
			t.Errorf(".ui-table-cell lost %q — a crowded column starts wrapping inside the row instead of truncating", want)
		}
	}
	if !strings.Contains(cssRule(t, ".ui-row-cells"), "min-width: 0") {
		t.Error(".ui-row-cells must shrink, or the cells inside it never get the chance to")
	}
	// Below 800px the row stacks and each cell gets the full width, so there
	// wrapping is right — but it has to break long words, not overflow.
	narrow := cssRule(t, ".ui-row-cells > .ui-table-cell")
	if !strings.Contains(narrow, "white-space: normal") || !strings.Contains(narrow, "word-break: break-word") {
		t.Error("the stacked phone layout must wrap and break words, not clip them")
	}
}
