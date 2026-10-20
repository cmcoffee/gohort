package ui

// A dialog's own buttons sit in its footer beside Close.
//
// A dialog's Save (or Stop, or Apply) used to be appended to the end of its
// content, and Close was in the footer under it: two button rows, the one
// that saves scrolling with the fields and the one that leaves pinned below.
// The audit's working window had Cancel in its content and Close beneath; the
// compose chooser had Cancel and Send in its content and Close beneath, so two
// of its three buttons closed it. addAction puts a mount's button in the
// footer, after Close.
//
// Driven rather than read: the real uiOpenModal and uiOpenSimpleModal, run
// over a stub DOM, and what is asserted is where the buttons ended up.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestADialogsButtonsShareItsFooter(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	prelude := readRuntimeFile(t, "00_prelude.js")
	from := strings.Index(prelude, "  window.uiOpenModal = function(opts) {")
	to := strings.Index(prelude, "  // window.uiOpenArtifactPane")
	if from < 0 || to < from {
		t.Fatal("could not find uiOpenModal and uiOpenSimpleModal in the prelude")
	}
	script := modalStubDOM + prelude[from:to] + `
function footerOf(dlg) { return dlg.children.filter(function(c) { return c.className === 'ui-modal-footer'; })[0]; }
function labels(bar) { return bar.children.map(function(b) { return b.textContent + (/primary/.test(b.className) ? '*' : ''); }).join(','); }

// A Save: after Close in the footer, and the main button, not in the content.
var save = null, body = null;
var m = window.uiOpenSimpleModal({title: 'Edit', mount: function(b, dlg) {
  body = b;
  save = document.createElement('button'); save.className = 'ui-row-btn primary'; save.textContent = 'Save';
  dlg.addAction(save);
}});
var bar = footerOf(m.dialog);
if (labels(bar) !== 'Close,Save*') throw new Error('the footer reads ' + labels(bar));
if (body.children.indexOf(save) >= 0) throw new Error('Save stayed in the content');

// A Stop while something runs: beside Close, which stays the way out, and
// gone from the row when it is removed.
var stop = null;
m = window.uiOpenSimpleModal({title: 'Audit', mount: function(b, dlg) {
  stop = document.createElement('button'); stop.className = 'ui-row-btn'; stop.textContent = 'Stop';
  dlg.addAction(stop);
}});
bar = footerOf(m.dialog);
if (labels(bar) !== 'Close*,Stop') throw new Error('the running footer reads ' + labels(bar));
stop.remove();
if (labels(bar) !== 'Close*') throw new Error('a removed Stop is still in the row: ' + labels(bar));

// A dialog with no footer of its own gets one for the button.
var api = window.uiOpenModal({title: 'Bare', actions: [], mount: function(b, a) {
  var go = document.createElement('button'); go.className = 'ui-row-btn primary'; go.textContent = 'Go';
  a.addAction(go);
}});
bar = footerOf(api.dialog);
if (!bar || labels(bar) !== 'Go*') throw new Error('a footerless dialog put its button ' + (bar ? labels(bar) : 'nowhere'));
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "modal_footer.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("a dialog's buttons are not in its footer:\n%s", out)
	}
}

// modalStubDOM is enough of a DOM for uiOpenModal to build a dialog and for a
// test to read where its buttons went.
const modalStubDOM = `
function node(tag) {
  var n = {
    tagName: String(tag).toUpperCase(), type: '', textContent: '', className: '', style: {}, children: [], isConnected: true,
    appendChild: function(c) { if (c.parentNode) c.remove(); this.children.push(c); c.parentNode = this; return c; },
    removeChild: function(c) { c.remove(); return c; },
    remove: function() { var p = this.parentNode; if (p) { var i = p.children.indexOf(this); if (i >= 0) p.children.splice(i, 1); this.parentNode = null; } },
    setAttribute: function() {}, getAttribute: function() { return null; },
    addEventListener: function() {}, focus: function() {},
    querySelector: function() { return null; }, querySelectorAll: function() { return []; },
    contains: function() { return false; },
  };
  return n;
}
global.document = {
  createElement: node, body: node('body'), activeElement: null,
  addEventListener: function() {}, removeEventListener: function() {},
  querySelectorAll: function() { return []; },
};
global.window = {
  uiNextModalZ: function() { return 1000; }, uiReleaseModalZ: function() {},
  matchMedia: function() { return {matches: false}; },
};
`
