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
