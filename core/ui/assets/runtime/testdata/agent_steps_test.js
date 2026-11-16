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
  n.querySelector = function(sel) {
    if (sel === ':scope > .ui-agent-tools-panel') return n.kids.find(function(k) { return k.attrs && k.attrs.class === 'ui-agent-tools-panel'; }) || null;
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
