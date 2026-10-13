package orchestrate

// The guardrails modal's "Also scan" / "Never scan" lists, driven rather than
// read. They were typed boxes, so a misspelled tool name saved fine and matched
// nothing; they are pickers over the tools the agent can call.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanToolListsArePickers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readFile(t, "assets/web_assets.html")
	const open = "          function scanListEditor(title, arr, help, pickTools) {"
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatal("scanListEditor has been reshaped; this test no longer reads it")
	}
	end := strings.Index(src[i:], "\n          }\n")
	if end < 0 {
		t.Fatal("scanListEditor never closes at its own indent")
	}
	fn := src[i : i+end+len("\n          }")]

	harness := `
function fail(m) { console.log('FAIL ' + m); process.exit(1); }
function node(tag) {
  return {tagName: tag, value: '', textContent: '', className: '', type: '', style: {cssText: ''},
    children: [], innerHTML: '',
    appendChild: function(c) { this.children.push(c); return c; }};
}
global.document = {createElement: node};
var gToolChoices = [];
var scanRenders = [];
function renderCovers() {}
` + fn + `
var arr = ['fetch_url', 'old_tool'];
var box = scanListEditor('Also scan', arr, 'help', true);
var list = box.children[1];
list.innerHTML = '';
if (list.children[0].children[0].tagName !== 'input') fail('before the tool list arrives a row should be typed');

gToolChoices = ['fetch_url', 'read_file', 'web_search'];
list.children = [];
scanRenders.forEach(function(fn) { fn(); });
var row0 = list.children[0].children[0], row1 = list.children[1].children[0];
if (row0.tagName !== 'select') fail('rows did not become pickers once the tool list arrived');
var opts0 = row0.children.map(function(o) { return o.value; });
if (opts0.indexOf('read_file') < 0) fail('a callable tool is missing from the picker: ' + opts0);
if (opts0.indexOf('old_tool') >= 0) fail('a name already picked in another row is offered again');
var labels1 = row1.children.map(function(o) { return o.textContent; });
if (labels1.indexOf('old_tool (not available)') < 0) fail('a stored name the agent cannot call was not kept and flagged: ' + labels1);

var trusted = scanListEditor('Trusted', ['wiki.internal'], 'help', false);
trusted.children[1].children = [];
scanRenders.forEach(function(fn) { fn(); });
if (trusted.children[1].children[0].children[0].tagName !== 'input') fail('the hosts list became a tool picker');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "scan.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The two boxes: network tools on the left, exceptions on the right, and a tool
// moved across and back lands where it started. A tool added to the scan by
// hand before this layout stays visible, marked, rather than scanned unseen.
func TestScanBoxesMoveToolsBetweenScannedAndExceptions(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readFile(t, "assets/web_assets.html")
	const open = "          // Two boxes, one rule."
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatal("the scan boxes have been reshaped; this test no longer reads them")
	}
	const close = "A call with no address in it is scanned.'));\n          renderCovers();\n"
	end := strings.Index(src[i:], close)
	if end < 0 {
		t.Fatal("the scan boxes block never ends where expected")
	}
	block := src[i : i+end+len(close)]

	harness := `
function fail(m) { console.log('FAIL ' + m); process.exit(1); }
function node(tag) {
  var n = {tagName: tag, value: '', textContent: '', className: '', type: '', title: '', style: {cssText: ''},
    children: [], appendChild: function(c) { this.children.push(c); return c; }};
  Object.defineProperty(n, 'innerHTML', {set: function() { this.children = []; }, get: function() { return ''; }});
  return n;
}
global.document = {createElement: node};
var gToolChoices = ['fetch_url', 'web_search', 'file_store'];
var agent = {scan_tools_add: ['file_store'], scan_tools_skip: [], scan_trusted_sources: []};
var scanDetail = node('div');
` + block + `
scanNetwork = ['fetch_url', 'web_search']; scanLoaded = true;
renderCovers(); scanRenders.forEach(function(fn) { fn(); });
function leftNames() { return leftList.children.map(function(r) { return r.children[0].textContent; }); }
var names = leftNames();
if (names.join('|') !== 'fetch_url|web_search|file_store (added by hand)') fail('left box: ' + names);

leftList.children[0].children[1].onclick();
if (scanSkip.join() !== 'fetch_url') fail('the exception was not recorded: ' + scanSkip);
if (leftNames().indexOf('fetch_url') >= 0) fail('an exception is still listed as scanned');
var toolsList = rightBox.children[2].children[1];
var excRow = toolsList.children[0];
if (excRow.children[0].value !== 'fetch_url') fail('the exception is not shown on the right');

excRow.children[1].onclick();
if (scanSkip.length) fail('removing the exception left it in the list');
if (leftNames()[0] !== 'fetch_url') fail('a removed exception did not go back to the scanned side: ' + leftNames());

var handAdded = leftList.children[2];
handAdded.children[1].onclick();
if (scanAdd.length) fail('a hand-added tool could not be taken off');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "boxes.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%v\n%s", err, out)
	}
}
