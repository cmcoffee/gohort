package ui

// Anything that fetches before it can show puts up uiLoading, not a bare
// "Loading…": a static word cannot be told apart from a hung one. The mark
// spins, shows its seconds once past three, and its one shared ticker stops
// when the last mark is gone.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadingMarkIsAlive(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "00_prelude.js")
	i := strings.Index(src, "  var LOADING_FRAMES =")
	j := strings.Index(src, "  window.uiLoading = uiLoading;")
	if i < 0 || j < 0 {
		t.Fatal("uiLoading is gone")
	}
	harness := `
var now = 1000000, intervals = {}, nextId = 1, marks = [];
Date.now = function() { return now; };
global.setInterval = function(f) { var id = nextId++; intervals[id] = f; return id; };
global.clearInterval = function(id) { delete intervals[id]; };
function el(tag, attrs, kids) {
  var n = {attrs: attrs || {}, kids: [], textContent: ''};
  (kids || []).forEach(function(k) { if (typeof k === 'string') n.textContent += k; else n.kids.push(k); });
  n.firstChild = n.kids[0]; n.lastChild = n.kids[n.kids.length - 1];
  return n;
}
global.window = {};
global.document = {querySelectorAll: function() { return marks; }};
` + src[i:j] + `
var m = uiLoading();
marks.push(m);
if (m.attrs.class !== 'ui-loading') throw new Error('class: ' + m.attrs.class);
if (Object.keys(intervals).length !== 1) throw new Error('the ticker did not start');
var first = m.firstChild.textContent;
now += 100; intervals[Object.keys(intervals)[0]]();
if (m.firstChild.textContent === first) throw new Error('the spinner did not move');
if (m.lastChild.textContent !== '') throw new Error('seconds shown before three');
now += 4000; intervals[Object.keys(intervals)[0]]();
if (m.lastChild.textContent !== '4s') throw new Error('seconds wrong: ' + m.lastChild.textContent);
if (uiLoading('Saving', {immediate: true}).attrs.class !== 'ui-loading now') throw new Error('immediate not honoured');
if (Object.keys(intervals).length !== 1) throw new Error('a second ticker started');
marks = []; intervals[Object.keys(intervals)[0]]();
if (Object.keys(intervals).length !== 0) throw new Error('the ticker outlived the last mark');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "loading.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("uiLoading does not hold:\n%s", out)
	}
	// And the runtime does not go back to the static word.
	if strings.Contains(runtimeJS, "'Loading…'") {
		t.Error("a bare 'Loading…' is back in the runtime; use uiLoading()")
	}
}
