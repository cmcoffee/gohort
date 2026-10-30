// Co-author (47_coauthor.js): highlight a passage the way you would to copy
// it and the chat is about it, quick actions and all; review what the agent
// proposes for that passage as a diff, applied or not by the person. Runs the shipped
// fragment and core/editor's diff against a small DOM stub.
var fs = require('fs');
var dir = __dirname + '/..';
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// --- a small DOM ------------------------------------------------------------
function node(tag) {
  var n = {tag: tag, attrs: {}, children: [], style: {}, on: {}, parentNode: null, textContent: '', disabled: false};
  n.setAttribute = function(k, v) { n.attrs[k] = v; };
  n.hasAttribute = function(k) { return k in n.attrs; };
  n.removeAttribute = function(k) { delete n.attrs[k]; };
  n.addEventListener = function(t, f) { (n.on[t] = n.on[t] || []).push(f); };
  n.fire = function(t, e) { (n.on[t] || []).forEach(function(f) { f(e || {}); }); };
  n.appendChild = function(c) { c.parentNode = n; n.children.push(c); return c; };
  n.insertBefore = function(c, ref) {
    c.parentNode = n;
    var i = ref ? n.children.indexOf(ref) : -1;
    if (i < 0) n.children.push(c); else n.children.splice(i, 0, c);
    return c;
  };
  n.removeChild = function(c) { n.children.splice(n.children.indexOf(c), 1); c.parentNode = null; };
  n.closest = function(sel) {
    var attr = sel.replace(/^\[|\]$/g, '');
    for (var p = n; p; p = p.parentNode) if (p.attrs && attr in p.attrs) return p;
    return null;
  };
  n.querySelector = function(sel) {
    var attr = sel.replace(/^\[|\]$/g, '');
    var stack = n.children.slice();
    while (stack.length) {
      var c = stack.shift();
      if (c.attrs && attr in c.attrs) return c;
      stack = stack.concat(c.children || []);
    }
    return null;
  };
  Object.defineProperty(n, 'innerHTML', {set: function(v) { n.children = []; n.html = v; }, get: function() { return n.html || ''; }});
  return n;
}
function el(tag, opts, kids) {
  var n = node(tag);
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else if (k === 'html') n.innerHTML = opts[k];
    else if (k.indexOf('on') === 0) n.addEventListener(k.slice(2), opts[k]);
    else if (k === 'style') n.style.cssText = opts[k];
    else n.setAttribute(k, opts[k]);
  }
  (kids || []).forEach(function(c) {
    if (c == null) return;
    n.appendChild(typeof c === 'string' ? Object.assign(node('#text'), {textContent: c}) : c);
  });
  if (opts && /display:none/.test(opts.style || '')) n.style.display = 'none';
  return n;
}
function text(n) {
  return (n.textContent || '') + n.children.map(text).join('');
}
function click(btn) { btn.fire('click'); }
function find(n, pred) {
  if (pred(n)) return n;
  for (var i = 0; i < n.children.length; i++) { var f = find(n.children[i], pred); if (f) return f; }
  return null;
}
function button(n, label) { return find(n, function(c) { return c.tag === 'button' && text(c) === label; }); }

var renderers = {};
var composers = [];
var docListeners = {};
var document = {body: node('body'), activeElement: null,
  querySelectorAll: function(sel) { return sel === '[data-ui-composer]' ? composers : []; },
  addEventListener: function(t, f) { (docListeners[t] = docListeners[t] || []).push(f); },
  removeEventListener: function(t, f) { docListeners[t] = (docListeners[t] || []).filter(function(g) { return g !== f; }); },
};
function selectionChanged() { (docListeners.selectionchange || []).forEach(function(f) { f({}); }); }
var window = {
  innerWidth: 800,
  addEventListener: function() {},
  uiRegisterBlockRenderer: function(name, fn) { renderers[name] = fn; },
};
global.Event = function(type, init) { this.type = type; this.bubbles = init && init.bubbles; };
global.setTimeout = function(f) { f(); };

// The diff ships ahead of the runtime (core/ui/runtime.go), its functions global.
var diffSrc = fs.readFileSync(dir + '/../../../editor/static/diff.js', 'utf8');
new Function('window', diffSrc + '\nwindow.editorDiffStats = editorDiffStats; window.editorDiffRender = editorDiffRender; window.editorDiffOps = editorDiffLines; window.editorDiffLines = editorDiffLines; window.editorDiffEscape = editorDiffEscape;')(window);
var src = fs.readFileSync(dir + '/47_coauthor.js', 'utf8');
var lib = new Function('el', 'window', 'document', src +
  '\nreturn {locate: coauthorLocate, replace: coauthorReplace, label: coauthorLabel, parts: coauthorReviewParts};')(el, window, document);

// --- the pure pieces ----------------------------------------------------------
var doc = 'la la\nverse one\nla la\nchorus\n';
check('a passage is found where it is', lib.locate(doc, 'verse one', 0) === 6);
check('a repeated passage resolves to the one nearest the selection', lib.locate(doc, 'la la', 15) === 16 && lib.locate(doc, 'la la', 0) === 0);
check('a passage that is gone is not found', lib.locate(doc, 'bridge', 0) === -1);
check('no passage is the whole document', lib.locate(doc, '', 9) === 0);
check('the replacement goes where the passage was', lib.replace(doc, 'la la', 16, 'oh oh') === 'la la\nverse one\noh oh\nchorus\n');
check('a whole-document proposal is the replacement', lib.replace(doc, '', 0, 'new') === 'new');
check('a chip names the passage by its opening words and lines',
  lib.label('Neon on the water\nCity never sleeps\n') === '“Neon on the water City never…”, 2 lines');
check('one line is just its words', lib.label('Neon on the water') === '“Neon on the water”');
var pr = lib.parts('a\nb c d\ne\n', 'c', 4, 'C');
check('a review splits on whole lines around the change',
  pr.before === 'a\n' && pr.oldMid === 'b c d\n' && pr.newMid === 'b C d\n' && pr.after === 'e\n');
pr = lib.parts('a\nb\nc\n', 'b\n', 2, 'B\n');
check('a passage ending at a line end takes just its lines', pr.before === 'a\n' && pr.oldMid === 'b\n' && pr.after === 'c\n');
pr = lib.parts('a\nlast', 'last', 2, 'end');
check('and the last line, with no newline after it', pr.oldMid === 'last' && pr.newMid === 'end' && pr.after === '');
pr = lib.parts('all of it', '', 0, 'new');
check('a whole-document proposal is all middle', pr.before === '' && pr.oldMid === 'all of it' && pr.newMid === 'new');

// --- a page: the document and its chat in one host ---------------------------
var host = el('div');
var calls = [];
var chat = el('div', {'data-ui-composer': ''});
chat.uiComposer = {
  setChip: function(label, onClear, o) { calls.push('chip:' + label); chat.onClear = onClear; chat.actions = (o && o.actions) || []; },
  clearChip: function() { calls.push('clear'); },
  focus: function() { calls.push('focus'); },
  insertText: function(t) { calls.push('insert:' + t); },
  send: function() { calls.push('send'); },
};
composers = [chat];
var ta = el('textarea');
ta.value = 'Verse one line\nNeon on the water\nCity never sleeps\nChorus line\n';
ta.focus = function() {};
ta.setSelectionRange = function(s, e) { ta.selectionStart = s; ta.selectionEnd = e; };
ta.getBoundingClientRect = function() { return {left: 0, top: 100, width: 400}; };
ta.dispatchEvent = function(e) { ta.fire(e.type, e); };
var pane = el('div');
pane.appendChild(ta);
host.appendChild(pane);
host.appendChild(chat);
host.uiSendContext = function() { return {editor: {code: ta.value}}; };

var shown = [];
var co = window.uiCoauthor(ta, {actions: [{label: 'Tighten', prompt: 'Tighten this.'}],
  showDocument: function() { shown.push('doc'); }});
check('the document finds the host it shares with its chat', host.attrs['data-ui-coauthor'] === '' && typeof host.uiProposeText === 'function');
check('nothing is put on the page beside the document', document.body.children.length === 0);
function select(start, end) { ta.selectionStart = start; ta.selectionEnd = end; }

// A drag over two lines.
var passage = 'Neon on the water\nCity never sleeps\n';
select(ta.value.indexOf(passage), ta.value.indexOf(passage) + passage.length);
ta.fire('mouseup', {});
check('highlighting a passage shows it in the chat',
  calls[0] === 'chip:Highlighted: “Neon on the water City never…”, 2 lines');
check('with the page\'s quick actions beside it', chat.actions.length === 1 && chat.actions[0].label === 'Tighten');
check('and the document keeps its place: nothing jumps to the chat', shown.length === 0 && calls.length === 1);
var ctx = host.uiSendContext();
check('the send carries the highlighted passage', ctx.selection && ctx.selection.text === passage && ctx.selection.start === 15);
check('beside what the host already sends', ctx.editor && ctx.editor.code === ta.value);
ta.fire('mouseup', {});
check('the same highlight again changes nothing', calls.length === 1);

// Moving to the chat to type keeps it: only the document's selection counts.
document.activeElement = {tag: 'textarea'};
select(0, 0);
selectionChanged();
check('a selection change somewhere else keeps the passage', co.pinned() !== null);
select(ta.value.indexOf(passage), ta.value.indexOf(passage) + passage.length);
document.activeElement = ta;

// An edit above moves the passage; the pin follows it.
ta.value = 'New first line\n' + ta.value;
ta.fire('input');
check('an edit elsewhere moves the pin with the passage', co.pinned().start === 30);

// A quick action sends its words about the passage at once.
calls = [];
chat.actions[0].onClick();
check('a quick action sends its words at once', calls.join('|') === 'insert:Tighten this.|send');
check('about the passage still highlighted', co.pinned() !== null);

// Clicking into the document with nothing selected lets it go.
calls = [];
select(5, 5);
ta.fire('mouseup', {});
check('a click with nothing highlighted lets the passage go', co.pinned() === null && calls.join() === 'clear');
check('and the send is about the whole document again', !host.uiSendContext().selection);
// A phone's handles move the selection without touching the textarea.
calls = [];
select(ta.value.indexOf(passage), ta.value.indexOf(passage) + 4);
selectionChanged();
check('a selection moved by a phone\'s handles is followed', co.pinned() && co.pinned().text === 'Neon');
var keep = ta.value.indexOf(passage);
select(keep, keep + passage.length);
ta.fire('keyup', {key: 'Shift'});
check('and one made with the keyboard', co.pinned().text === passage);

// --- a proposal for that passage ---------------------------------------------
var before = ta.value;
var r = host.uiProposeText({find: passage, replace: 'Neon on the river\nCity never sleeps\n', summary: 'Water to river'});
var review = find(pane, function(c) { return /ui-coauthor-review\b/.test(c.className || '') && !/-h|-body|-note/.test(c.className); });
check('a proposal brings the document up to review it', shown.join() === 'doc');
check('a proposal opens its review in the document\'s place', r.ok && review && pane.children.indexOf(review) === 0 && ta.style.display === 'none');
var reviewHTML = find(review, function(c) { return /editor-diff/.test(c.html || ''); }).html;
check('showing the change where it goes, among the lines around it',
  /editor-diff-equal"><span class="editor-diff-marker"> <\/span><span class="editor-diff-text">Verse one line/.test(reviewHTML) &&
  /editor-diff-remove[^]*Neon on the water/.test(reviewHTML) && /editor-diff-add[^]*Neon on the river/.test(reviewHTML) &&
  /editor-diff-equal[^]*Chorus line/.test(reviewHTML));
check('and its counts', /\+1 −1/.test(text(review)));
check('a changed line reads old then new', reviewHTML.indexOf('editor-diff-remove') < reviewHTML.indexOf('editor-diff-add'));
var ops = window.editorDiffOps('a\nb\nc\n', 'a\nB\nc\nd\n');
check('and in a longer diff, each change in place, old before new',
  ops.map(function(o) { return o.type[0] + o.text; }).join(',') === 'ea,rb,aB,ec,ad,e');
check('nothing is applied before Apply', ta.value === before);

calls = [];
var inputs = 0;
ta.addEventListener('input', function() { inputs++; });
click(button(review, 'Apply'));
check('Apply writes the replacement where the passage was',
  ta.value === 'New first line\nVerse one line\nNeon on the river\nCity never sleeps\nChorus line\n');
check('and tells the page the document changed', inputs === 1);
check('and the conversation is about the whole document again', calls.indexOf('clear') >= 0 && co.pinned() === null);
calls = [];
ta.fire('mouseup', {});
check('showing the applied passage is not the person highlighting it', co.pinned() === null && calls.length === 0);
var undo = find(pane, function(c) { return c.className === 'ui-coauthor-undo'; });
check('an Undo stays after Apply', undo && !find(pane, function(c) { return /ui-coauthor-review/.test(c.className || ''); }));
check('and the document is back in its place', ta.style.display === '');
click(button(undo, 'Undo'));
check('Undo puts it back', ta.value === before);

// A passage edited since the proposal came is not written over.
host.uiProposeText({find: 'Chorus line', replace: 'Chorus, louder', summary: 'Louder'});
review = find(pane, function(c) { return /ui-coauthor-review\b/.test(c.className || '') && !/-h|-body|-note/.test(c.className); });
ta.value = ta.value.replace('Chorus line', 'Chorus I rewrote');
click(button(review, 'Apply'));
check('Apply refuses a passage that changed since, and says so',
  ta.value.indexOf('Chorus I rewrote') >= 0 && /changed since/.test(text(review)) && button(review, 'Apply').disabled);
check('a passage that is not there at all is refused up front',
  host.uiProposeText({find: 'no such line', replace: 'x'}).ok === false);

// Deleting the pinned passage lets the pin go.
// (A moment later: what the review selected was not the person's doing.)
var realNow = Date.now;
Date.now = function() { return realNow() + 1000; };
ta.selectionStart = ta.value.indexOf('Verse one line');
ta.selectionEnd = ta.selectionStart + 'Verse one line'.length;
ta.fire('mouseup', {});
check('a highlight after the review is the person\'s again', co.pinned() && co.pinned().text === 'Verse one line');
calls = [];
ta.value = ta.value.replace('Verse one line', '');
ta.fire('input');
check('removing the pinned passage unpins it', co.pinned() === null && calls.indexOf('clear') >= 0);

// --- going away ---------------------------------------------------------------
// An editor that closes and opens again builds a new one each time: the old
// one hands the host back as it found it, or the second wraps the first.
var host2 = el('div');
var pane2 = el('div');
var ta2 = el('textarea');
ta2.value = 'one two three';
ta2.focus = function() {};
ta2.setSelectionRange = function(s, e) { ta2.selectionStart = s; ta2.selectionEnd = e; };
pane2.appendChild(ta2);
host2.appendChild(pane2);
var chat2 = el('div', {'data-ui-composer': ''});
var cleared2 = 0;
chat2.uiComposer = {setChip: function() {}, clearChip: function() { cleared2++; }, focus: function() {}, insertText: function() {}, send: function() {}};
host2.appendChild(chat2);
composers = [chat, chat2];
function own() { return {document: {text: ta2.value}}; }
host2.uiSendContext = own;
host2.setAttribute('data-ui-send-context', '');
var listenersBefore = (docListeners.selectionchange || []).length;
var co2 = window.uiCoauthor(ta2, {host: host2});
co2.pin({start: 4, end: 7, text: 'two'});
host2.uiProposeText({find: 'two', replace: '2'});
co2.destroy();
check('destroy hands the host its own send context back', host2.uiSendContext === own && 'data-ui-send-context' in host2.attrs);
check('and stops answering proposals', host2.uiProposeText === undefined && !('data-ui-coauthor' in host2.attrs));
check('and lets the pin go, chip and all', co2.pinned() === null && cleared2 === 1);
check('and closes its review', !find(pane2, function(c) { return /ui-coauthor-review/.test(c.className || ''); }) && ta2.style.display === '');
check('and stops following the page\'s selection', (docListeners.selectionchange || []).length === listenersBefore);
var bare = el('div');
var ta3 = el('textarea');
bare.appendChild(ta3);
window.uiCoauthor(ta3, {host: bare}).destroy();
check('a host that had no send context of its own is left without one', !('data-ui-send-context' in bare.attrs) && bare.uiSendContext === undefined);

// --- the proposal's card ------------------------------------------------------
var proposed = [];
var cardHost = el('div', {'data-ui-coauthor': ''});
cardHost.uiProposeText = function(p) { proposed.push(p); return p.find === 'gone' ? {ok: false, reason: 'It changed.'} : {ok: true}; };
var out = renderers.text_proposal({text: 'Tighter chorus', data: {find: 'a\nb\n', replace: 'c'}}, {live: true});
cardHost.appendChild(out.wrap);
proposed = [];
click(button(out.wrap, 'Review'));
check('the card names the change and how much it replaces', /Tighter chorus/.test(text(out.wrap)) && /Replaces 2 lines/.test(text(out.wrap)));
check('Review opens it in the document it sits beside', proposed.length === 1 && proposed[0].find === 'a\nb\n' && proposed[0].summary === 'Tighter chorus');
var gone = renderers.text_proposal({text: 'x', data: {find: 'gone', replace: 'y'}}, {});
cardHost.appendChild(gone.wrap);
click(button(gone.wrap, 'Review'));
check('a refused proposal says why on its card', /It changed\./.test(text(gone.wrap)));
var whole = renderers.text_proposal({text: 'Rewrite', data: {find: '', replace: 'all new'}}, {});
check('a whole-document proposal says so', /The whole document/.test(text(whole.wrap)));

if (fail) { console.log(fail + ' FAILED'); process.exit(1); }
