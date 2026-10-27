// An action registered on reply bubbles (uiRegisterBubbleAction) gets the
// bubble, its text, and the button that was pressed, so an action that takes
// a while (read aloud) can show its own progress on it.
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
  var node = {tag: tag, attrs: attrs || {}, kids: [], textContent: (kids || []).join(''),
    dataset: {}, classList: {contains: function() { return false; }},
    appendChild: function(c) { this.kids.push(c); },
    get lastElementChild() { return this.kids[this.kids.length - 1] || null; }};
  return node;
}
var window = {};
var activeSessionId = 'room-1';
var msgEls = {};

var bubbleActionRegistry = {user: [], assistant: []};
eval('window.uiRegisterBubbleAction = ' + lift(panel, 'window.uiRegisterBubbleAction = function(opts)', 'uiRegisterBubbleAction').replace('window.uiRegisterBubbleAction = ', ''));
eval(lift(panel, 'function appendBubbleActions(bar, role, bubble)', 'appendBubbleActions'));

var got = null;
window.uiRegisterBubbleAction({role: 'assistant', label: 'Act', title: 'Do it', onclick: function(ctx) { got = ctx; }});
var bubble = {textContent: 'The reply.', dataset: {}};
msgEls.m1 = {bubble: bubble, rawText: 'The **reply**.'};
var bar = el('div');
appendBubbleActions(bar, 'assistant', bubble);
var btn = bar.kids[0];
check('the action is a button in the bar', btn && btn.tag === 'button' && btn.textContent === 'Act');
btn.attrs.onclick();
check('pressing it hands over the bubble', got && got.bubble === bubble);
check('and its raw text', got && got.getText() === 'The **reply**.');
check('and the button that was pressed', got && got.button === btn);
process.exit(fail ? 1 : 0);
