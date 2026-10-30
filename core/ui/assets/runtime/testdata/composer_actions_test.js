// A page script can put its own button in the chat's input row
// (uiRegisterComposerAction) and hear when a message goes out and when the
// turn it started is over (ui-agent-sent, ui-agent-turn-done): what the
// dictation mic and its read-the-reply-aloud are built on.
var fs = require('fs');
var dir = __dirname + '/..';
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(src, start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}
var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

function el(tag, attrs, kids) {
  attrs = attrs || {};
  return {tag: tag, attrs: attrs, textContent: (kids || []).join(''), on: {}, dataset: {},
    classList: {contains: function(c) { return (attrs['class'] || '').split(' ').indexOf(c) >= 0; }},
    addEventListener: function(t, f) { this.on[t] = f; }};
}
var events = [];
var window = {dispatchEvent: function(e) { events.push(e); }};
global.CustomEvent = function(type, init) { this.type = type; this.detail = init && init.detail; };

var focused = 0;
var inputArea = {value: '', style: {}, scrollHeight: 40, focus: function() { focused++; }};
var sendBtn = {name: 'send', style: {}};
var attachBtn = {name: 'attach'};
function box(kids) {
  var b = {kids: kids, insertBefore: function(n, ref) {
    var i = ref == null ? -1 : this.kids.indexOf(ref);
    if (i < 0) this.kids.push(n); else this.kids.splice(i, 0, n);
  }};
  b.childNodes = b.kids;
  return b;
}
function mode(name) { return {name: name, dataset: {}}; }
// The row's three places: the left end, the middle (holding the mode
// switches), and Attach and Send on the right.
var composerMiddle = box([mode('private'), mode('fresh')]);
composerMiddle.name = 'middle';
var inputRow = box([composerMiddle, attachBtn, sendBtn]);
function names(b) { return b.kids.map(function(k) { return k.name || k.textContent; }).join(','); }
var convoPane = {clientWidth: 0, clientHeight: 0};
eval(lift(fs.readFileSync(dir + '/06_assist_editor.js', 'utf8'), 'function growComposer(', 'growComposer'));
eval(lift(panel, 'function autosizeInput()', 'autosizeInput'));
eval(lift(panel, 'function composerInsert(text)', 'composerInsert'));
eval('window.uiRegisterComposerAction = ' + lift(panel, 'window.uiRegisterComposerAction = function(opts)', 'uiRegisterComposerAction').replace('window.uiRegisterComposerAction = ', ''));

var pressed = null;
var ctx = window.uiRegisterComposerAction({label: 'M', title: 'Dictate', onclick: function(c) { pressed = c; }});
check('a plain button goes in the middle, after the switches', names(composerMiddle) === 'private,fresh,M' && names(inputRow) === 'middle,attach,send');
check('with its label and title', ctx.button.textContent === 'M' && ctx.button.attrs.title === 'Dictate');
ctx.button.on.click();
check('pressing it hands over its ctx', pressed === ctx && pressed.button === ctx.button && pressed.input === inputArea);
ctx.insertText('  good morning ');
check('text goes into the box', inputArea.value === 'good morning');
check('which is resized and focused', inputArea.style.height === '42px' && focused === 1);
ctx.insertText('and more');
check('more text joins what is typed with a space', inputArea.value === 'good morning and more');
inputArea.value = 'line one\n';
ctx.insertText('two');
check('no extra space after a line break', inputArea.value === 'line one\ntwo');
ctx.insertText('   ');
check('nothing to add changes nothing', inputArea.value === 'line one\ntwo');
var sent = 0;
function sendMessage() { sent++; }
ctx.send();
check('send() sends what is in the box, as Send does', sent === 1);
var extraInputs = {agent_id: {value: 'a-1'}, scope: {value: 'old'}};
function hostSendContext() { return {scope: 'doc-7'}; }
var f = ctx.fields();
check('fields() is what a send carries beside the message, the host context last',
  f.agent_id === 'a-1' && f.scope === 'doc-7' && Object.keys(f).length === 2);
check('an action without an onclick is refused', window.uiRegisterComposerAction({label: 'x'}) === null && composerMiddle.kids.length === 3);

// prominent takes a button to the left-hand end; first puts one at the
// start of the middle, ahead of the switches; each place keeps the order its
// buttons were registered in.
var noop = function() {};
var mic = window.uiRegisterComposerAction({label: 'mic', first: true, prominent: true, onclick: noop});
var spk = window.uiRegisterComposerAction({label: 'spk', first: true, onclick: noop});
var spk2 = window.uiRegisterComposerAction({label: 'spk2', first: true, onclick: noop});
var plain = window.uiRegisterComposerAction({label: 'plain', onclick: noop});
check('the prominent one leads the row, Attach and Send end it', names(inputRow) === 'mic,middle,attach,send');
check('first ones lead the middle, plain ones follow the switches', names(composerMiddle) === 'spk,spk2,private,fresh,M,plain');
check('prominent marks the one a page means people to reach for first',
  mic.button.classList.contains('ui-agent-composer-act-lead') && !spk.button.classList.contains('ui-agent-composer-act-lead'));

eval(lift(panel, 'function announceSent(text)', 'announceSent'));
announceSent('the message');
check('a send is announced with its text', events.length === 1 && events[0].type === 'ui-agent-sent' && events[0].detail.text === 'the message');

// enableInput is the end of a turn.
var recordLocked = false, statusPill = null, activeStream = null, activeEventSource = null, turnLive = true;
var cancelBtn = {style: {}}, cancelLabel = {};
function clearThinking() {}
function stopHeartbeat() {}
function refreshPins() {}
eval(lift(panel, 'function enableInput()', 'enableInput'));
enableInput();
check('the end of a turn is announced', events.length === 2 && events[1].type === 'ui-agent-turn-done');

// Both ways a message goes out announce it.
var send = lift(panel, 'function sendMessage()', 'sendMessage');
check('the queued-note send announces itself', /inputArea\.value = '';\s*announceSent\(text\);/.test(send));
check('the ordinary send announces itself', /inputArea\.style\.height = '';\s*announceSent\(text\);\s*disableInput\(\);/.test(send));

// Enter sends on a keyboard, Shift+Enter is a new line; on a touch screen's
// own keyboard return is a new line and the send button sends; an Enter that
// picks an input method's candidate belongs to the input method.
var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
eval(lift(prelude, 'function uiComposerEnterSends(ev)', 'uiComposerEnterSends'));
var touch = false;
window.matchMedia = function(q) { return {matches: touch && q === '(hover: none) and (pointer: coarse)'}; };
check('Enter sends', uiComposerEnterSends({key: 'Enter'}) === true);
check('Shift+Enter is a new line', uiComposerEnterSends({key: 'Enter', shiftKey: true}) === false);
check('another key sends nothing', uiComposerEnterSends({key: 'a'}) === false);
check('Enter picking a candidate is the input method\'s', uiComposerEnterSends({key: 'Enter', isComposing: true}) === false);
check('so is the 229 some browsers send instead', uiComposerEnterSends({key: 'Enter', keyCode: 229}) === false);
touch = true;
check('a touch keyboard\'s return is a new line', uiComposerEnterSends({key: 'Enter'}) === false);
var panelChat = fs.readFileSync(dir + '/20_chat_panel.js', 'utf8');
check('the agent composer asks it', /if \(uiComposerEnterSends\(ev\)\) \{ ev\.preventDefault\(\); sendMessage\(\); \}/.test(panel));
check('so does the chat composer', /if \(uiComposerEnterSends\(ev\)\) \{\s*ev\.preventDefault\(\);\s*doSend\(\);/.test(panelChat));
// The panel's own composer, for the component it sits in (uiCoauthor): the
// same box and send, and a chip over the box saying what the next send is about.
(function() {
  var a = panel.indexOf("var composerChip = el('div'");
  var b = panel.indexOf("composerChip.style.display = 'none';\n      },\n    };", a);
  if (a < 0 || b < 0) { check('the panel composer block is there', false); return; }
  var block = panel.slice(a, b + "composerChip.style.display = 'none';\n      },\n    };".length);
  function node(tag, attrs, kids) {
    var n = {tag: tag, attrs: attrs || {}, style: {}, kids: [], on: {}};
    if (attrs && attrs.style === 'display:none') n.style.display = 'none';
    n.appendChild = function(c) { n.kids.push(c); };
    Object.defineProperty(n, 'innerHTML', {set: function() { n.kids = []; }});
    (kids || []).forEach(function(k) { n.kids.push(k); });
    if (attrs && attrs.onclick) n.on.click = attrs.onclick;
    return n;
  }
  var pane = box([]);
  var row = {name: 'row'};
  pane.kids.push(row);
  var wrapAttrs = {};
  var wrap = {setAttribute: function(k, v) { wrapAttrs[k] = v; }};
  var sent = 0;
  var area = {value: 'hi', style: {}, scrollHeight: 40, focus: function() {}};
  // The chip is drawn by the one filler every chat's composer shares.
  var assist = fs.readFileSync(dir + '/06_assist_editor.js', 'utf8');
  var fill = new Function('el', lift(assist, 'function fillComposerChip(', 'fillComposerChip') + '\nreturn fillComposerChip;')(node);
  check('the assist chat draws its chip the same way',
    assist.indexOf('fillComposerChip(chip, label, onClear, o, function() { right.uiComposer.clearChip(); });') >= 0);
  new Function('el', 'convoPane', 'inputRow', 'wrap', 'composerInsert', 'sendMessage', 'inputArea', 'fillComposerChip', block)(
    node, pane, row, wrap, function(t) { area.value += ' ' + t; }, function() { sent++; }, area, fill);
  var chip = pane.kids[0];
  check('the chip row sits over the input row, hidden', pane.kids[1] === row && chip.style.display === 'none');
  check('the panel says it has a composer', 'data-ui-composer' in wrapAttrs && typeof wrap.uiComposer === 'object');
  var cleared = 0;
  wrap.uiComposer.setChip('Chorus, 4 lines', function() { cleared++; });
  check('a chip shows its label', chip.style.display === '' && chip.kids[0].kids[0] === 'Chorus, 4 lines');
  wrap.uiComposer.setChip('Verse 2', function() { cleared++; });
  check('a second chip replaces the first', chip.kids.length === 2 && chip.kids[0].kids[0] === 'Verse 2');
  chip.kids[1].on.click();
  check('its x clears it and tells its owner', chip.style.display === 'none' && cleared === 1);
  var acted = [];
  wrap.uiComposer.setChip('Highlighted: “Neon”', null, {actions: [{label: 'Tighten', onClick: function() { acted.push('t'); }}, {label: ''}]});
  check('a chip carries its actions as buttons, between the label and the x',
    chip.kids.length === 3 && chip.kids[1].kids[0] === 'Tighten' && chip.kids[2].kids[0] === '\u00d7');
  chip.kids[1].on.click();
  check('an action button does what it says', acted.join() === 't');
  wrap.uiComposer.setChip('x', function() { cleared++; });
  wrap.uiComposer.clearChip();
  check('clearChip clears without calling back', chip.style.display === 'none' && cleared === 1);
  wrap.uiComposer.insertText('tighten');
  wrap.uiComposer.send();
  check('insertText and send are the box\'s own', area.value === 'hi tighten' && sent === 1);
})();
// A chat's input grows with what is typed: about eight lines in a wide chat
// (the CSS cap), up to half the conversation in a narrow one, where the same
// message wraps onto many more lines.
(function() {
  var assist = fs.readFileSync(dir + '/06_assist_editor.js', 'utf8');
  var grow = new Function(lift(assist, 'function growComposer(', 'growComposer') + '\nreturn growComposer;')();
  function box(scroll) { return {style: {}, scrollHeight: scroll}; }
  var ta = box(300);
  grow(ta, {clientWidth: 380, clientHeight: 800});
  check('a narrow chat lets its box grow to half the conversation', ta.style.maxHeight === '400px' && ta.style.height === '302px');
  grow(ta, {clientWidth: 380, clientHeight: 240});
  check('never below the eight lines a wide one has', ta.style.maxHeight === '192px');
  grow(ta, {clientWidth: 900, clientHeight: 800});
  check('a wide chat keeps its cap', ta.style.maxHeight === '');
  grow(ta, {clientWidth: 0, clientHeight: 0});
  check('one not laid out yet keeps its cap', ta.style.maxHeight === '');
  check('the agent chat sizes its box this way', panel.indexOf('function autosizeInput() { growComposer(inputArea, convoPane); }') >= 0);
  check('and so does the assist chat', assist.indexOf("composer.addEventListener('input', function() { growComposer(composer, right); });") >= 0);
})();
process.exit(fail ? 1 : 0);
