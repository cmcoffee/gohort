// Co-author (47_coauthor.js): select a passage the way you would to copy it,
// Ask about it or run a quick action, and review what the agent proposes for
// that passage as a diff, applied or not by the person. Runs the shipped
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
var document = {body: node('body'), querySelectorAll: function(sel) { return sel === '[data-ui-composer]' ? composers : []; }};
var window = {
  innerWidth: 800,
  addEventListener: function() {},
  uiRegisterBlockRenderer: function(name, fn) { renderers[name] = fn; },
};
global.Event = function(type, init) { this.type = type; this.bubbles = init && init.bubbles; };
global.setTimeout = function(f) { f(); };

// The diff ships ahead of the runtime (core/ui/runtime.go), its functions global.
var diffSrc = fs.readFileSync(dir + '/../../../editor/static/diff.js', 'utf8');
new Function('window', diffSrc + '\nwindow.editorDiffStats = editorDiffStats; window.editorDiffRender = editorDiffRender; window.editorDiffOps = editorDiffLines;')(window);
var src = fs.readFileSync(dir + '/47_coauthor.js', 'utf8');
var lib = new Function('el', 'window', 'document', src +
  '\nreturn {locate: coauthorLocate, replace: coauthorReplace, label: coauthorLabel};')(el, window, document);

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

// --- a page: the document and its chat in one host ---------------------------
var host = el('div');
var calls = [];
var chat = el('div', {'data-ui-composer': ''});
chat.uiComposer = {
  setChip: function(label, onClear) { calls.push('chip:' + label); chat.onClear = onClear; },
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
  showChat: function() { shown.push('chat'); }, showDocument: function() { shown.push('doc'); }});
check('the document finds the host it shares with its chat', host.attrs['data-ui-coauthor'] === '' && typeof host.uiProposeText === 'function');
var bar = document.body.children[0];
check('the selection bar starts hidden', bar.style.display === 'none');

// A drag over two lines.
var passage = 'Neon on the water\nCity never sleeps\n';
ta.selectionStart = ta.value.indexOf(passage);
ta.selectionEnd = ta.selectionStart + passage.length;
ta.fire('mouseup', {clientX: 200, clientY: 300});
check('a selection shows the bar, with Ask and the page\'s quick actions',
  bar.style.display === '' && button(bar, 'Ask') && button(bar, 'Tighten'));
check('the bar sits over the selection', bar.style.left === (200 - 80) + 'px' && bar.style.top === (300 - 36 - 10) + 'px');

ta.fire('keyup', {key: 'Escape'});
check('Escape dismisses the bar', bar.style.display === 'none');
ta.fire('mouseup', {clientX: 200, clientY: 300});
click(button(bar, 'Ask'));
check('Ask pins the passage with a chip and focuses the chat',
  calls[0] === 'chip:“Neon on the water City never…”, 2 lines' && calls[1] === 'focus' && bar.style.display === 'none');
check('Ask brings the chat up where only one shows at a time', shown.join() === 'chat');
var ctx = host.uiSendContext();
check('the send carries the pinned passage', ctx.selection && ctx.selection.text === passage && ctx.selection.start === 15);
check('beside what the host already sends', ctx.editor && ctx.editor.code === ta.value);

// An edit above moves the passage; the pin follows it.
ta.value = 'New first line\n' + ta.value;
ta.fire('input');
check('an edit elsewhere moves the pin with the passage', co.pinned().start === 30);

// A quick action pins and sends at once.
calls = [];
ta.fire('mouseup', {clientX: 200, clientY: 300});
click(button(bar, 'Tighten'));
check('a quick action sends its words at once', calls.join('|').indexOf('insert:Tighten this.|send') >= 0);
check('and stays on the document', shown.join() === 'chat');

// --- a proposal for that passage ---------------------------------------------
var before = ta.value;
var r = host.uiProposeText({find: passage, replace: 'Neon on the river\nCity never sleeps\n', summary: 'Water to river'});
var review = find(pane, function(c) { return c.className === 'ui-coauthor-review'; });
check('a proposal brings the document up to review it', shown.join() === 'chat,doc');
check('a proposal opens a review beside the document', r.ok && review && pane.children.indexOf(review) === 1);
check('showing the passage\'s diff, not the whole document\'s',
  /editor-diff-remove/.test(review.html || find(review, function(c) { return /editor-diff/.test(c.html || ''); }).html) &&
  !/Chorus line/.test(find(review, function(c) { return /editor-diff/.test(c.html || ''); }).html));
check('and its counts', /\+1 −1/.test(text(review)));
var diffHTML = find(review, function(c) { return /editor-diff/.test(c.html || ''); }).html;
check('a changed line reads old then new', diffHTML.indexOf('editor-diff-remove') < diffHTML.indexOf('editor-diff-add'));
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
var undo = find(pane, function(c) { return c.className === 'ui-coauthor-undo'; });
check('an Undo stays after Apply', undo && !find(pane, function(c) { return c.className === 'ui-coauthor-review'; }));
click(button(undo, 'Undo'));
check('Undo puts it back', ta.value === before);

// A passage edited since the proposal came is not written over.
host.uiProposeText({find: 'Chorus line', replace: 'Chorus, louder', summary: 'Louder'});
review = find(pane, function(c) { return c.className === 'ui-coauthor-review'; });
ta.value = ta.value.replace('Chorus line', 'Chorus I rewrote');
click(button(review, 'Apply'));
check('Apply refuses a passage that changed since, and says so',
  ta.value.indexOf('Chorus I rewrote') >= 0 && /changed since/.test(text(review)) && button(review, 'Apply').disabled);
check('a passage that is not there at all is refused up front',
  host.uiProposeText({find: 'no such line', replace: 'x'}).ok === false);

// Deleting the pinned passage lets the pin go.
ta.selectionStart = ta.value.indexOf('Verse one line');
ta.selectionEnd = ta.selectionStart + 'Verse one line'.length;
ta.fire('mouseup', {clientX: 10, clientY: 10});
click(button(bar, 'Ask'));
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
var barsBefore = document.body.children.length;
var co2 = window.uiCoauthor(ta2, {host: host2});
co2.pin({start: 4, end: 7, text: 'two'});
host2.uiProposeText({find: 'two', replace: '2'});
co2.destroy();
check('destroy hands the host its own send context back', host2.uiSendContext === own && 'data-ui-send-context' in host2.attrs);
check('and stops answering proposals', host2.uiProposeText === undefined && !('data-ui-coauthor' in host2.attrs));
check('and lets the pin go, chip and all', co2.pinned() === null && cleared2 === 1);
check('and closes its review', !find(pane2, function(c) { return c.className === 'ui-coauthor-review'; }));
check('and takes its bar off the page', document.body.children.length === barsBefore);
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
