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

	// What is asserted is what reached send().
	harness := assistStubDOM + `
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

// assistStubDOM is enough of a DOM for the workbench to mount and for the
// mount callback's timeout to run.
const assistStubDOM = `
var listeners = {};
function node(tag) {
  return {
    tagName: tag, value: '', textContent: '', className: '', disabled: false,
    style: {}, children: [],
    appendChild: function(c) { this.children.push(c); return c; },
    removeChild: function(c) {}, remove: function() {},
    setAttribute: function() {}, getAttribute: function() { return null; },
    addEventListener: function(k, fn) { (listeners[k] = listeners[k] || []).push(fn); (this['__' + k] = this['__' + k] || []).push(fn); },
    querySelector: function() { return null; }, querySelectorAll: function() { return []; },
    focus: function() {}, scrollHeight: 0, scrollTop: 0, classList: classes(),
  };
}
function classes() {
  var set = {};
  return {
    add: function(c) { set[c] = true; }, remove: function(c) { delete set[c]; },
    contains: function(c) { return !!set[c]; },
    toggle: function(c, on) { if (on === undefined) on = !set[c]; if (on) set[c] = true; else delete set[c]; return on; },
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
window.uiOpenModal = function(o) { mounted = o; o.mount(document.body, {overlay: node('div'), dialog: node('div')}); };
`

// On a narrow screen the conversation and the draft take turns as tabs.
// Talking it through opens on the conversation; a version written while the
// conversation shows marks the Draft tab rather than pulling the reply out
// of view; opening the tab clears the mark. Revising text that is already
// there opens on that text.
func TestAssistTabsFollowTheWork(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + `
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
var reply = null;
window.uiOpenAssist({
  mode: 'discuss', initial: '',
  send: function(req, done) { reply = done; },
});
var wrap = find(document.body, 'ui-assist');
var tabs = find(wrap, 'ui-assist-tabs').children, chatTab = tabs[0], draftTab = tabs[1];
if (!wrap.classList.contains('ui-assist-on-chat')) throw new Error('discuss should open on the conversation');
if (!chatTab.classList.contains('active')) throw new Error('the Conversation tab should be the active one');
var composer = find(wrap, 'ui-assist-input');
composer.value = 'go ahead';
fire(find(wrap, 'ui-assist-send'));
reply({reply: 'written', value: 'new text'});
if (!wrap.classList.contains('ui-assist-on-chat')) throw new Error('a new version pulled the conversation out of view');
if (!draftTab.classList.contains('ui-assist-tab-new')) throw new Error('the Draft tab was not marked');
fire(draftTab);
if (!wrap.classList.contains('ui-assist-on-draft')) throw new Error('the Draft tab did not show the draft');
if (draftTab.classList.contains('ui-assist-tab-new')) throw new Error('opening the draft did not clear its mark');

document.body.children = [];
window.uiOpenAssist({initial: 'text already here', send: function() {}});
if (!find(document.body, 'ui-assist').classList.contains('ui-assist-on-draft')) throw new Error('revising existing text should open on it');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "tabs.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist tabs:\n%s", out)
	}
}

// A switchable window lets the person pick how it works, and each send says
// which: talking it through when nothing is written yet, just writing when
// there is text to revise or an opening request, and a switch mid-
// conversation takes effect on the next send. A window that does not offer
// the switch has none, and a pinned mode wins over switchable.
func TestAssistModeSwitch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + `
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
var modes = [];
function open(o) {
  document.body.children = [];
  o.send = function(req, done) { modes.push(req.mode); done({reply: 'ok'}); };
  window.uiOpenAssist(o);
  return find(document.body, 'ui-assist');
}
function say(wrap, text) {
  find(wrap, 'ui-assist-input').value = text;
  fire(find(wrap, 'ui-assist-send'));
}

var wrap = open({switchable: true, initial: ''});
var row = find(wrap, 'ui-assist-modes');
if (!row) throw new Error('a switchable window shows no switch');
var talk = row.children[0], just = row.children[1];
if (!talk.classList.contains('active')) throw new Error('an empty field should open on Talk it through');
var write = find(wrap, 'ui-assist-write');
if (!write || write.style.display === 'none') throw new Error('Write it now should show while talking it through');
say(wrap, 'one');
fire(just);
if (write.style.display !== 'none') throw new Error('Write it now should hide under Just write');
say(wrap, 'two');
fire(talk);
say(wrap, 'three');
if (modes.join(',') !== 'discuss,write,discuss') throw new Error('sends went out as ' + modes.join(','));

modes = [];
wrap = open({switchable: true, initial: 'some text'});
if (!find(wrap, 'ui-assist-modes').children[1].classList.contains('active')) throw new Error('existing text should open on Just write');
say(wrap, 'x');
if (modes[0] !== 'write') throw new Error('existing text sent as ' + modes[0]);

wrap = open({switchable: true, initial: '', ask: 'shorten it'});
if (!find(wrap, 'ui-assist-modes').children[1].classList.contains('active')) throw new Error('an opening request should open on Just write');

wrap = open({initial: ''});
if (find(wrap, 'ui-assist-modes')) throw new Error('a window that did not offer the switch shows one');
if (find(wrap, 'ui-assist-write')) throw new Error('a write-only window shows Write it now');

wrap = open({switchable: true, mode: 'discuss', initial: 'text'});
if (find(wrap, 'ui-assist-modes')) throw new Error('a pinned mode should not show the switch');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "modes.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist mode switch:\n%s", out)
	}
}
