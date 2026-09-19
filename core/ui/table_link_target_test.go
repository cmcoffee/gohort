package ui

// Where a link in a table goes.
//
// Driven, because the whole defect is a single attribute: the anchor is built,
// the href is right, the page it reaches is right, and it opens in the wrong
// place. Nothing errors and the destination is correct, so only watching the
// element get built catches it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A cell link into this deployment navigates in place. Forcing a tab strands
// the back chevron (a fresh tab has no history, so it falls through to the
// declared parent) and leaves the list you came from open behind it.
func TestAnInAppCellLinkDoesNotOpenATab(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "10_basics.js")
	i := strings.Index(src, "var href = col.link ? lookup(rec, col.link) : null;")
	if i < 0 {
		t.Fatal("the cell-link branch is gone")
	}
	end := strings.Index(src[i:], "cellsWrap.appendChild(cell);")
	if end < 0 {
		t.Fatal("could not bound the cell-link branch")
	}
	branch := src[i : i+end]

	prelude := readRuntimeFile(t, "00_prelude.js")
	j := strings.Index(prelude, "window.uiLeavesTheApp = function(href) {")
	if j < 0 {
		t.Fatal("uiLeavesTheApp is gone")
	}
	k := strings.Index(prelude[j:], "\n  };")
	leaves := prelude[j : j+k+len("\n  };")]

	harness := `
global.location = {href: 'https://host.example.test/somewhere', origin: 'https://host.example.test'};
global.URL = require('url').URL;
global.window = {};
` + leaves + `
var uiLeavesTheApp = window.uiLeavesTheApp;
function el(tag, attrs) {
  var n = {tagName: tag, textContent: '', children: [],
           appendChild: function(c) { this.children.push(c); return c; }};
  Object.keys(attrs || {}).forEach(function(k) { n[k] = attrs[k]; });
  return n;
}
function fmt(v) { return String(v == null ? '' : v); }
function lookup(rec, f) { return rec[f]; }

function build(href) {
  var col = {link: 'u', format: null};
  var rec = {u: href};
  var v = 'click me';
  var cell = el('td', {});
  ` + branch + `
  return cell.children[0];
}

var cases = [
  ['/section/editor?id=1', false, 'an in-app path'],
  ['https://host.example.test/section/editor', false, 'the same origin spelled absolutely'],
  ['https://elsewhere.example.test/doc', true, 'another origin'],
];
cases.forEach(function(c) {
  var a = build(c[0]);
  if (!a || a.tagName !== 'a') { console.log('FAIL no anchor for ' + c[0]); process.exit(1); }
  var opened = a.target === '_blank';
  if (opened !== c[1]) {
    console.log('FAIL ' + c[2] + ' (' + c[0] + '): target=' + a.target + ', want ' + (c[1] ? '_blank' : 'same tab'));
    process.exit(1);
  }
  // An external link that opens a tab must not hand it a live opener.
  if (c[1] && a.rel !== 'noopener') { console.log('FAIL no rel=noopener on ' + c[0]); process.exit(1); }
});
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "link.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%s", out)
	}
}

// The same component answered "where does a link go" two ways depending on
// whether you clicked the row or the cell. RowLink has always navigated in
// place; this keeps them from drifting apart again.
func TestARowLinkAndACellLinkAgree(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	i := strings.Index(src, "var rowHref = cfg.row_link")
	if i < 0 {
		t.Fatal("the row-link branch is gone")
	}
	rowBranch := src[i : i+1200]
	// The row navigates in place and only opens a tab on a modifier click.
	if !strings.Contains(rowBranch, "window.location.href = href") {
		t.Error("RowLink no longer navigates in place")
	}
	if !strings.Contains(rowBranch, "newTab") {
		t.Error("RowLink no longer honours a modifier click")
	}
	// And the cell link must not be unconditionally _blank again.
	j := strings.Index(src, "var href = col.link ? lookup(rec, col.link) : null;")
	cellBranch := src[j : j+1400]
	if strings.Contains(cellBranch, "target: '_blank'") {
		t.Error("the cell link forces a new tab again, so an in-app link strands the back chevron")
	}
	if !strings.Contains(cellBranch, "uiLeavesTheApp(") {
		t.Error("the cell link no longer asks whether the destination leaves the app")
	}
}
