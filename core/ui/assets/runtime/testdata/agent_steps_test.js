// Steps shown as a turn runs (AgentLoopPanel.ShowSteps): a running step has a
// spinner and its seconds, a finished one a tick, a failed one a cross; an
// app's words replace the name and arguments; a step that ends is redrawn in
// place, staying open when it was opened.
var fs = require('fs');
var dir = __dirname + '/..';
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(src, start, end, label) {
  var i = src.indexOf(start), j = src.indexOf(end, i);
  if (i < 0 || j < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  return src.slice(i, j);
}
var src = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

function node(tag, attrs, kids) {
  var n = {tag: tag, attrs: attrs || {}, kids: [], _attr: {}, open: false, isConnected: true,
    classList: {contains: function(c) { return (n.attrs.class || '').split(' ').indexOf(c) >= 0; }}};
  (kids || []).forEach(function(k) { n.kids.push(typeof k === 'string' ? {text: k} : k); });
  n.appendChild = function(c) { n.kids.push(c); c.parentNode = n; };
  n.replaceChild = function(a, b) { var i = n.kids.indexOf(b); n.kids[i] = a; a.parentNode = n; };
  n.insertBefore = function(a, b) {
    var at = n.kids.indexOf(a);
    if (at >= 0) n.kids.splice(at, 1); // the DOM moves a node it already holds
    var i = n.kids.indexOf(b);
    n.kids.splice(i < 0 ? n.kids.length : i, 0, a);
    a.parentNode = n;
  };
  n.setAttribute = function(k, v) { n._attr[k] = v; };
  Object.defineProperty(n, 'firstChild', {get: function() { return n.kids[0] || null; }});
  Object.defineProperty(n, 'nextElementSibling', {get: function() {
    var sib = n.parentNode && n.parentNode.kids; return sib ? sib[sib.indexOf(n) + 1] || null : null; }});
  n.removeChild = function(c) { var i = n.kids.indexOf(c); if (i >= 0) n.kids.splice(i, 1); c.parentNode = null; };
  n.querySelector = function(sel) {
    if (sel === ':scope > .ui-agent-tools-panel') return n.kids.find(function(k) { return k.attrs && k.attrs.class === 'ui-agent-tools-panel'; }) || null;
    if (sel === ':scope > .ui-agent-msg-body') return n.kids.find(function(k) { return k.attrs && k.attrs.class === 'ui-agent-msg-body'; }) || null;
    return null;
  };
  return n;
}
function el(tag, attrs, kids) { return node(tag, attrs, kids); }
function text(n) { return (n.kids || []).map(function(k) { return k.text != null ? k.text : text(k); }).join(' '); }
var cfg = {show_steps: true};
var convoLog = node('div');
convoLog.querySelectorAll = function() { return []; };
var thinkingEl = null;
function setLiveText() {}
function toolArgsHover() { return ''; }
function toolArgsBlock() { return null; }
var window = {UIStepSummaries: {}};
var setInterval = function() { return 1; };
var clearInterval = function() {};

eval(lift(src, '    function renderAgentToolEntry(t) {', '    // findToolEntry finds a call by id at any depth.', 'the step functions'));

window.UIStepSummaries.read_file = function(args, out) {
  return 'Read ' + args.path + (out == null ? '' : ' (' + out.split('\n').length + ' lines)');
};
var running = {name: 'read_file', args: 'path="a.go"', argsFull: {path: 'a.go'}, output: null};
var line = text(renderAgentToolEntry(running));
check('a running step has a spinner', line.indexOf('⠋') >= 0);
check('a running step uses the app’s words', line.indexOf('Read a.go') >= 0 && line.indexOf('read_file') < 0);
running.output = 'one\ntwo';
check('a finished step has a tick and its words see the output', text(renderAgentToolEntry(running)).indexOf('✓') >= 0 && text(renderAgentToolEntry(running)).indexOf('(2 lines)') >= 0);
var failed = {name: 'run', args: 'command="x"', argsFull: {command: 'x'}, output: 'error: exit status 1'};
var fl = text(renderAgentToolEntry(failed));
check('a failed step has a cross and, with no words registered, its name', fl.indexOf('✗') >= 0 && fl.indexOf('run') >= 0);

var asked = text(renderAgentToolEntry({name: 'Approval: Run a command in X?', args: 'detail="command: ls"', argsFull: {detail: 'command: ls'}, output: 'allowed'}));
check('an approval reads as its question', asked.indexOf('Asked: Run a command in X?') >= 0 && asked.indexOf('detail=') < 0);

thinkingEl = node('div');
convoLog.appendChild(thinkingEl);
var host = turnStepsHost();
check('the turn’s steps go above the working dots', convoLog.kids.indexOf(host) === convoLog.kids.indexOf(thinkingEl) - 1);
check('one list for the turn', turnStepsHost() === host);
convoLog.insertBefore(node('div', {class: 'a card'}), thinkingEl);
turnStepsHost();
check('a new step brings the list back above the dots', convoLog.kids.indexOf(host) === convoLog.kids.indexOf(thinkingEl) - 1 && convoLog.kids.length === 3);
var step = {name: 'read_file', args: '', argsFull: {path: 'b.go'}, output: null};
host.tools.push(step);
renderTurnSteps(host);
var panel = host.querySelector(':scope > .ui-agent-tools-panel');
var first = panel.kids[0];
first.open = true;
renderTurnSteps(host);
check('an unchanged step is not redrawn', panel.kids[0] === first);
step.output = 'x';
renderTurnSteps(host);
check('a step that ends is redrawn in place, still open', panel.kids.length === 1 && panel.kids[0] !== first && panel.kids[0].open === true);
// Routine steps an app grouped share one line, consecutive ones together,
// that opens to them; the rest keep a line each, and a failure breaks the
// group. An opened group stays open as it grows. A finished turn folds
// behind one line saying what it did; a turn of one step does not fold.
window.UIStepGroupOf = {read_file: 'look', search_code: 'look'};
window.UIStepGroupWords = {look: function(steps) { return 'Looked ' + steps.length + ' times'; }};
window.UITurnSummary = function(steps) { return steps.filter(function(s) { return s.name === 'edit_file'; }).length + ' edits'; };
var t2 = turnStepsHost();
t2.tools = [];
function call(name, id, out) { return {name: name, call_id: id, args: '', argsFull: {path: id}, output: out}; }
t2.tools.push(call('read_file', 'r1', 'a'), call('search_code', 'r2', 'b'), call('edit_file', 'e1', 'ok'), call('read_file', 'r3', 'c'));
renderTurnSteps(t2);
var p2 = t2.querySelector(':scope > .ui-agent-tools-panel');
check('two routine steps share one line, in the app\u2019s words', p2.kids.length === 3 && /ui-agent-step-group/.test(p2.kids[0].attrs.class) && text(p2.kids[0]).indexOf('Looked 2 times') >= 0);
check('an edit keeps its own line', !/ui-agent-step-group/.test(p2.kids[1].attrs.class) && text(p2.kids[1]).indexOf('edit_file') >= 0);
check('one routine step on its own is a plain line', !/ui-agent-step-group/.test(p2.kids[2].attrs.class));
p2.kids[0].open = true;
t2.tools.push(call('search_code', 'r4', null));
renderTurnSteps(t2);
check('the last routine steps group as one more arrives, spinning while it runs', p2.kids.length === 3 && text(p2.kids[2]).indexOf('Looked 2 times') >= 0 && text(p2.kids[2]).indexOf('\u280b') >= 0);
check('an opened group stays open across a redraw', p2.kids[0].open === true);
t2.tools[3] = call('read_file', 'r3', 'error: no such file'); // it failed as it finished
t2.tools[4].output = 'found';
renderTurnSteps(t2);
check('a failed step breaks the group and keeps its own line', p2.kids.length === 4 && text(p2.kids[2]).indexOf('\u2717') >= 0);
t2.stepsFolded = true;
renderTurnSteps(t2);
check('a finished turn folds behind one line saying what it did', p2.kids.length === 1 && /ui-agent-steps-fold/.test(p2.kids[0].attrs.class) && text(p2.kids[0]).indexOf('1 edits, 1 failed') >= 0 && text(p2.kids[0]).indexOf('steps') < 0);
var keepSummary = window.UITurnSummary;
window.UITurnSummary = null;
renderTurnSteps(t2);
check('with no words of the app\u2019s, the folded line counts the steps', text(p2.kids[0]).indexOf('5 steps, 1 failed') >= 0);
window.UITurnSummary = keepSummary;
var t3 = {tools: [call('edit_file', 'e9', 'ok')], stepsFolded: true, querySelector: function() { return null; }, appendChild: function(c) { this.panel = c; c.parentNode = this; }};
renderTurnSteps(t3);
check('a turn of one step is not folded', t3.panel.kids.length === 1 && !/ui-agent-steps-fold/.test(t3.panel.kids[0].attrs.class || ''));

// A list runs until the agent writes to the person: the next step after a
// reply with text starts a new list below it, and the one before folds. An
// empty reply (a round that only called tools) or a card does not split it.
var replyWith = function(words) {
  var b = node('div', {class: 'ui-agent-msg ui-agent-msg-assistant' + (words ? '' : ' ui-agent-msg-empty')});
  var body = node('div', {class: 'ui-agent-msg-body'}, words ? [words] : []);
  body.textContent = words;
  b.appendChild(body);
  return b;
};
convoLog.kids = [];
turnSteps = null;
thinkingEl = node('div');
convoLog.appendChild(thinkingEl);
var first = turnStepsHost();
first.tools.push(call('read_file', 'x1', 'ok'), call('read_file', 'x2', 'ok'));
convoLog.insertBefore(replyWith(''), thinkingEl);
convoLog.insertBefore(node('div', {class: 'a card'}), thinkingEl);
check('a round with no words, or a card, keeps the list going', turnStepsHost() === first);
convoLog.insertBefore(replyWith('Found it: the handler is in web.go.'), thinkingEl);
var second = turnStepsHost();
check('after the agent writes, the next step starts a new list below its words', second !== first &&
  convoLog.kids.indexOf(second) === convoLog.kids.indexOf(thinkingEl) - 1 && convoLog.kids.indexOf(first) < convoLog.kids.length - 3);
check('the list before it folds behind its line', first.stepsFolded === true);
check('the new list keeps going until the agent writes again', turnStepsHost() === second);

// The status line: which tier answered and how much of its window the
// prompt took, in the warning colour from four fifths full; null clears it.
var meterEl = {textContent: '', title: '', style: {color: ''}};
eval(lift(src, '    function setMeter(ev) {', '    function setStatus(text) {', 'setMeter'));
setMeter({tier: 'worker', model: 'small', context_tokens: 12400, context_size: 32000});
check('the meter names the tier and the share of the window', meterEl.textContent === 'Worker \u00b7 12k of 32k context' && /small: /.test(meterEl.title) && /39%/.test(meterEl.title));
check('under four fifths it is muted', meterEl.style.color === 'var(--text-mute)');
setMeter({tier: 'lead', context_tokens: 1500, context_size: 1800});
check('near full it warns', meterEl.textContent === 'Lead \u00b7 1.5k of 1.8k context' && /warning/.test(meterEl.style.color));
setMeter({tier: 'unset', context_tokens: 900, context_size: 0});
check('no tier or window: just what was used', meterEl.textContent === '900 context');
setMeter(null);
check('null clears it', meterEl.textContent === '' && meterEl.title === '');
process.exit(fail ? 1 : 0);
