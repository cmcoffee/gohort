package ui

// The assist workbench's opening request.
//
// A workbench opened with a brief — "settle this finding", "make it
// shorter" — used to have nowhere to put it: the caller could only
// write the brief into a subtitle and hope the person retyped it. Every
// such caller then invented its own seeding, or shipped a modal that
// opens and sits there.
//
// Driven rather than read, because the failure mode is silence: if the
// seed does not reach send(), nothing errors and nothing happens.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistOpensWithItsRequestAlreadySent(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	if !strings.Contains(src, "opts.ask") {
		t.Fatal("the workbench takes no opening request")
	}

	// A stub DOM: enough for the workbench to mount and for the mount
	// callback's timeout to run. What is asserted is what reached send().
	harness := `
var listeners = {};
function node(tag) {
  return {
    tagName: tag, value: '', textContent: '', className: '', disabled: false,
    style: {}, children: [],
    appendChild: function(c) { this.children.push(c); return c; },
    removeChild: function(c) {}, remove: function() {},
    setAttribute: function() {}, getAttribute: function() { return null; },
    addEventListener: function(k, fn) { (listeners[k] = listeners[k] || []).push(fn); },
    querySelector: function() { return null; }, querySelectorAll: function() { return []; },
    focus: function() {}, scrollHeight: 0, scrollTop: 0, classList: {add: function(){}, remove: function(){}},
  };
}
global.document = {
  createElement: node, createTextNode: function(t) { return {text: t}; },
  addEventListener: function() {}, body: node('body'),
  querySelector: function() { return null; }, querySelectorAll: function() { return []; },
};
global.window = {};
function el(tag, attrs, kids) {
  var n = node(tag);
  Object.keys(attrs || {}).forEach(function(k) {
    if (k === 'class') n.className = attrs[k];
    else if (typeof attrs[k] === 'function') n[k] = attrs[k];
    else n[k] = attrs[k];
  });
  (kids || []).forEach(function(k) { n.appendChild(k); });
  return n;
}
function mdToHTML(s) { return s; }
var mounted = null;
window.uiOpenModal = function(o) { mounted = o; o.mount(document.body); };

` + src + `

var sent = null;
window.uiOpenAssist({
  title: 'Rewrite',
  initial: 'the original text',
  ask: 'settle this finding',
  send: function(req, done) { sent = req; done({reply: 'done', value: 'the rewritten text'}); },
  onAccept: function(text) { global.accepted = text; },
});

setTimeout(function() {
  if (!sent) throw new Error('the opening request was never sent');
  if (sent.message !== 'settle this finding') throw new Error('wrong message: ' + sent.message);
  // It goes out with the draft it opened on, so the model revises rather
  // than writing from nothing.
  if (sent.draft !== 'the original text') throw new Error('the draft did not travel: ' + sent.draft);
  // First turn, so there is no history to account for.
  if (sent.history.length !== 0) throw new Error('history should start empty');

  // And accepting hands back the PROPOSAL, with the original still one
  // version-walk away — which is what makes accepting one safe.
  var accept = mounted.actions.filter(function(a) { return a.primary; })[0];
  accept.onClick({close: function() {}});
  if (global.accepted !== 'the rewritten text') throw new Error('accepted the wrong version: ' + global.accepted);
  console.log('OK');
}, 5);
`
	tmp := filepath.Join(t.TempDir(), "assist.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the opening request does not reach send():\n%s", out)
	}

	// Without one, nothing is sent — a blank workbench must stay blank
	// rather than asking the model to guess what was wanted.
	blank := strings.Replace(harness, "ask: 'settle this finding',", "", 1)
	blank = strings.Replace(blank, `if (!sent) throw new Error('the opening request was never sent');`,
		`if (sent) throw new Error('an unasked workbench should send nothing'); console.log('OK'); return;`, 1)
	tmp2 := filepath.Join(t.TempDir(), "blank.js")
	if err := os.WriteFile(tmp2, []byte(blank), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp2).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("a workbench with no request should stay quiet:\n%s", out)
	}
}
