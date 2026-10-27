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
var inputRow = {kids: [attachBtn, sendBtn], insertBefore: function(n, ref) {
  this.kids.splice(this.kids.indexOf(ref), 0, n);
}};
inputRow.childNodes = inputRow.kids;
eval(lift(panel, 'function autosizeInput()', 'autosizeInput'));
eval('window.uiRegisterComposerAction = ' + lift(panel, 'window.uiRegisterComposerAction = function(opts)', 'uiRegisterComposerAction').replace('window.uiRegisterComposerAction = ', ''));

var pressed = null;
var ctx = window.uiRegisterComposerAction({label: 'M', title: 'Dictate', onclick: function(c) { pressed = c; }});
check('the button sits in the input row before Send', inputRow.kids.length === 3 && inputRow.kids[1].tag === 'button' && inputRow.kids[2] === sendBtn);
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
check('an action without an onclick is refused', window.uiRegisterComposerAction({label: 'x'}) === null && inputRow.kids.length === 3);

// first puts a button at the start of the row's buttons, ahead of Attach and
// of buttons not put first, in the order such buttons were registered.
var noop = function() {};
var mic = window.uiRegisterComposerAction({label: 'mic', first: true, onclick: noop});
var spk = window.uiRegisterComposerAction({label: 'spk', first: true, onclick: noop});
var names = inputRow.kids.map(function(k) { return k.name || k.textContent; });
check('first buttons lead the row, in the order registered', names.join(',') === 'mic,spk,attach,M,send');

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
process.exit(fail ? 1 : 0);
