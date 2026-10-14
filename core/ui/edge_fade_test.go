package ui

// Rows that scroll sideways (the agent top bar's, tab bars, section rails)
// get uiEdgeFade, which marks the side a row has more on. Driven on a fake row: the marks follow the scroll,
// a row that fits carries none, and a mark it already has is never set again,
// since the panel watches class changes and re-setting one would wake that
// watcher forever.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEdgeFadeMarksTheSideWithMore(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "00_prelude.js")
	fn := regexp.MustCompile(`(?s)  window\.uiEdgeFade = function\(strip\) \{.*?\n  \};\n`).FindString(src)
	if fn == "" {
		t.Fatal("uiEdgeFade not found in the prelude")
	}
	fn = strings.Replace(fn, "window.uiEdgeFade = function(strip)", "function edgeFade(strip)", 1)
	script := `
var window = {addEventListener: function() {}};
var writes = 0;
function strip(scrollWidth, clientWidth) {
  var set = {}, handlers = {};
  return {
    scrollWidth: scrollWidth, clientWidth: clientWidth, scrollLeft: 0,
    addEventListener: function(k, f) { handlers[k] = f; },
    fire: function(k) { handlers[k](); },
    classList: {
      contains: function(c) { return !!set[c]; },
      toggle: function(c, on) { writes++; if (on) set[c] = true; else delete set[c]; },
    },
  };
}
var pending = [];
function setTimeout(f) { pending.push(f); }
` + fn + `
var s = strip(600, 300);
edgeFade(s);
pending.forEach(function(f) { f(); });
if (!s.classList.contains('ui-strip-more-right') || s.classList.contains('ui-strip-more-left')) throw new Error('at the start only the right side has more');
s.scrollLeft = 150; s.fire('scroll');
if (!s.classList.contains('ui-strip-more-right') || !s.classList.contains('ui-strip-more-left')) throw new Error('midway both sides have more');
s.scrollLeft = 300; s.fire('scroll');
if (s.classList.contains('ui-strip-more-right') || !s.classList.contains('ui-strip-more-left')) throw new Error('at the end only the left side has more');
var before = writes;
s.fire('scroll'); s.fire('scroll');
if (writes !== before) throw new Error('an unchanged mark was set again');
var fits = strip(200, 300);
edgeFade(fits);
pending.forEach(function(f) { f(); });
if (fits.classList.contains('ui-strip-more-right') || fits.classList.contains('ui-strip-more-left')) throw new Error('a row that fits should carry no mark');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "fade.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("edgeFade:\n%s", out)
	}
}
