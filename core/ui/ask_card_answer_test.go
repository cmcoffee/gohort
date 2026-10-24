package ui

// An answered ask card is the answer: it keeps what was picked, fills back in
// from the state its answer was sent with, and opens again to be changed. Its
// choices end on the card's own Other row, so an "Other" the model wrote into
// the options is dropped rather than drawn as a second one with no box.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnAskCardKeepsAndChangesItsAnswer(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	prelude := readRuntimeFile(t, "00_prelude.js")
	cards := readRuntimeFile(t, "35_ask_cards.js")
	var fns []string
	fns = append(fns, boundedRuntime(t, prelude, "  function el(", "\n  }\n"))
	for _, name := range []string{"uiAskQuestionHTML", "uiAskIsCatchAll", "uiAskOptions", "uiAskChoices", "uiAskChoiceText", "uiAskLock", "uiAskActions", "uiAskAnswered", "uiAskCardWrap"} {
		fns = append(fns, boundedRuntime(t, cards, "  function "+name+"(", "\n  }\n"))
	}
	fns = append(fns, boundedRuntime(t, cards, "  if (!window.UIBlockRenderers.ui_ask) {", "\n  }\n"))
	harness := `
function Node(tag) {
  var self = this;
  this.tagName = tag; this.children = []; this.listeners = {}; this.attrs = {};
  this.value = ''; this.checked = false; this.disabled = false; this.className = ''; this.dataset = {};
  this.classList = {
    has: function(c) { return (' ' + self.className + ' ').indexOf(' ' + c + ' ') >= 0; },
    contains: function(c) { return this.has(c); },
    add: function(c) { if (!this.has(c)) self.className = (self.className + ' ' + c).trim(); },
    remove: function(c) { self.className = (' ' + self.className + ' ').replace(' ' + c + ' ', ' ').trim(); },
    toggle: function(c, on) { if (on === undefined) on = !this.has(c); if (on) this.add(c); else this.remove(c); },
  };
}
Node.prototype.appendChild = function(c) { this.children.push(c); return c; };
Node.prototype.setAttribute = function(k, v) { this.attrs[k] = v; };
Node.prototype.addEventListener = function(k, f) { (this.listeners[k] = this.listeners[k] || []).push(f); };
Node.prototype.fire = function(k) { (this.listeners[k] || []).forEach(function(f) { f({}); }); };
Node.prototype.focus = function() { focused = this; };
Node.prototype.all = function(pred, out) {
  out = out || [];
  this.children.forEach(function(c) { if (c instanceof Node) { if (pred(c)) out.push(c); c.all(pred, out); } });
  return out;
};
Node.prototype.querySelectorAll = function() {
  return this.all(function(n) { return /^(input|textarea|select|button)$/.test(n.tagName); });
};
var focused = null;
var document = {createElement: function(t) { return new Node(t); }, createTextNode: function(t) { return {text: t}; }};
function textOf(n) { return n.children.map(function(c) { return c.text != null ? c.text : (c instanceof Node ? textOf(c) : ''); }).join(''); }
function button(card, label) { return card.all(function(n) { return n.tagName === 'button' && textOf(n) === label; })[0]; }
function boxes(card) { return card.all(function(n) { return n.tagName === 'input'; }); }
function otherText(card) { return card.all(function(n) { return n.classList.has('ui-ask-other-text'); })[0]; }

var sent = [];
function uiAskSubmitAnswer(card, answer, state) {
  sent.push({answer: answer, state: JSON.parse(JSON.stringify(state)), edit: card.classList.has('ui-ask-editing')});
  return true;
}
var window = {UIBlockRenderers: {}, uiRegisterBlockRenderer: function(n, f) { window.UIBlockRenderers[n] = f; }};
` + strings.Join(fns, "\n") + `
function check(ok, why) { if (!ok) throw new Error(why); }

// The catch-alls a model writes, and answers that only look like one.
['Other', 'other…', 'Something else', 'Other (please specify)', 'Other: type it'].forEach(function(s) {
  check(uiAskIsCatchAll(s), s + ' should be dropped for the Other row');
});
['None', 'Yes', 'Another option', 'Others on the team review it'].forEach(function(s) {
  check(!uiAskIsCatchAll(s), s + ' is a real answer');
});

// A card: the model's own "Other" is dropped, its Other row is last.
var render = window.UIBlockRenderers.ui_ask;
var card = render({id: 'q1', question: 'Which?', options: ['A', 'B', 'Other'], multi: true}).wrap;
var ins = boxes(card);
check(ins.length === 3, 'want A, B and the Other box, got ' + ins.length + ' boxes');
check(ins[2].value === '' && ins[0].value === 'A', 'the Other box should be last');

// Typing in the Other line ticks it, and it rides along with the picks.
ins[0].checked = true;
otherText(card).value = 'and C';
otherText(card).fire('input');
check(ins[2].checked, 'typing in the Other line should tick it');
button(card, 'Submit').fire('click');
check(sent.length === 1 && sent[0].answer === 'A. and C' && !sent[0].edit, 'submit sent ' + JSON.stringify(sent));
check(sent[0].state.picks.join() === 'A' && sent[0].state.other === 'and C', 'the state should say what was picked and typed');
check(card.classList.has('submitted') && card.classList.has('ui-ask-filled') && ins[0].disabled, 'an answered card locks and keeps its picks');
check(!button(card, 'Change answer').disabled, 'Change answer stays live on an answered card');

// Change, then Cancel: the earlier answer comes back, nothing is sent.
button(card, 'Change answer').fire('click');
check(card.classList.has('ui-ask-editing') && !ins[0].disabled, 'Change answer should open the card');
ins[0].checked = false; ins[1].checked = true;
button(card, 'Cancel').fire('click');
check(ins[0].checked && !ins[1].checked && ins[0].disabled && sent.length === 1, 'Cancel should put the answer back');

// Change, then Submit: the new answer goes as a change of the old one.
button(card, 'Change answer').fire('click');
ins[1].checked = true; ins[2].checked = false;
button(card, 'Submit').fire('click');
check(sent.length === 2 && sent[1].edit && sent[1].answer === 'A, B', 'a changed answer sent ' + JSON.stringify(sent[1]));
check(!card.classList.has('ui-ask-editing') && ins[1].disabled, 'the changed card locks again');

// A redraw fills the card in from the stored state.
var again = render({id: 'q2', question: 'Which?', options: ['A', 'B']}, {replayed: true, answer: {picks: [], other: 'neither'}}).wrap;
var b2 = boxes(again);
check(!b2[0].checked && !b2[1].checked && b2[2].checked && otherText(again).value === 'neither', 'the redraw should show the typed answer');
check(again.classList.has('ui-ask-filled'), 'a filled-in redraw shows only what was chosen');

// An answer that arrives from elsewhere (block_meta) fills the card in and
// locks it, and one the block carries does so as it is drawn.
var meta = render({id: 'q4', question: 'Which?', options: ['A', 'B']});
meta.onMeta(null, {answer: {picks: ['B'], other: ''}});
var b4 = boxes(meta.wrap);
check(b4[1].checked && b4[1].disabled && meta.wrap.classList.has('submitted'), 'a block_meta answer should fill and lock the card');
var carried = render({id: 'q5', question: 'Which?', options: ['A'], answer: {picks: ['A'], other: ''}}).wrap;
check(boxes(carried)[0].checked && carried.classList.has('submitted'), 'a block carrying its answer should draw answered');

// Nothing chosen: no send, and the Other line takes focus.
var empty = render({id: 'q3', question: 'Which?', options: ['A']}).wrap;
button(empty, 'Submit').fire('click');
check(sent.length === 2 && focused === otherText(empty), 'an empty answer should not send');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "ask.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("ask card:\n%s", out)
	}
}

// The panel's half: the answer's message carries the card's state, and the
// message is tied to its card, live and on a redraw, so changing the answer
// has the message to replace.
func TestThePanelTiesAnAnswerToItsCard(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	for _, want := range []string{
		"if (ask && ask.state) body.ask_state = ask.state;",
		"if (ask && msgEls[localMsgId]) linkAskAnswer(ask.card, msgEls[localMsgId].bubble);",
		"if (card && m.role === 'user' && m.ask_state && msgEls[mid]) linkAskAnswer(card, msgEls[mid].bubble);",
		"commitUserEdit(prior, d.answer || '')",
		"querySelectorAll('.ui-agent-msg, .ui-ask-card')",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
	css := string(runtimeCSS)
	if !strings.Contains(css, ".ui-agent-msg.ui-agent-ask-answer { display: none; }") {
		t.Error("a card's answer message is drawn as a bubble of its own again")
	}
}

// A card with an answer_url posts its answer there: a page with no chat
// to reply through, whose run is waiting on the card.
func TestACardWithAnAnswerURLPostsThere(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	cards := readRuntimeFile(t, "35_ask_cards.js")
	harness := boundedRuntime(t, cards, "  function uiAskSubmitAnswer(", "\n  }\n") + `
var posted = null;
function fetch(url, opts) { posted = {url: url, body: JSON.parse(opts.body)}; return {then: function() { return {catch: function() {}}; }}; }
function uiAskPanelRoot() { throw new Error('a card with an answer_url looked for a chat composer'); }
var card = {dataset: {answerUrl: 'api/scope/r1'}, classList: {contains: function() { return false; }}};
if (!uiAskSubmitAnswer(card, '1. When? -> May', {values: ['May']})) throw new Error('not sent');
if (!posted || posted.url !== 'api/scope/r1' || posted.body.answer !== '1. When? -> May' || posted.body.state.values[0] !== 'May') throw new Error('posted ' + JSON.stringify(posted));
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "post.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("answer_url:\n%s", out)
	}
}
