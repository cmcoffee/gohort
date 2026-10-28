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
eval(lift(panel, 'function autosizeInput()', 'autosizeInput'));
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
process.exit(fail ? 1 : 0);
