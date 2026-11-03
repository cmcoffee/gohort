// A reply's text is announced as it grows, is replaced, finishes, or is
// struck (ui-agent-message-text), so something can follow a reply while it is
// written (reading it aloud) rather than only once it is finished. Generic:
// the event carries the text and what happened to it, nothing about speech.
// Checked against the shipped source.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function body(header, next) { var i = src.indexOf(header); return i < 0 ? '' : src.slice(i, src.indexOf(next, i + header.length)); }

var announce = body('function announceText(m, id, what) {', '\n    }\n');
check('the event is named for what it says', announce.indexOf("'ui-agent-message-text'") > 0);
check('replies only', announce.indexOf("m.role !== 'assistant'") > 0);
check('carries the text and what happened to it', /text: m\.rawText \|\| ''/.test(announce) &&
  announce.indexOf('done: !!what.done') > 0 && announce.indexOf('replaced: !!what.replaced') > 0 && announce.indexOf('struck: !!what.struck') > 0);
check('and whether it is a replay, asked now (active is a function: unasked, it is always true)', announce.indexOf('replay: replayWindow.active(),') > 0);
check('as it grows', body('function appendChunk(id, text) {', 'function ').indexOf('announceText(m, id);') > 0);
check('when it is replaced', body('function replaceChunk(id, text) {', 'function ').indexOf('announceText(m, id, {replaced: true});') > 0);
check('when it finishes', body('function finalizeMessage(id) {', '// --- Inline tool-call rendering').indexOf('announceText(m, id, {done: true});') > 0);
check('when it is struck', body('function strikeMessage(id, reason) {', 'function ').indexOf('announceText(m, id, {struck: true});') > 0);
check('nothing in it about speech', !/speak|voice|audio/i.test(announce));

// Run, not only read: a live reply is not a replay, and a replayed one is.
// Reading the source passed a version that tested the function itself
// (always true), so every live update was dropped as a replay.
var events = [];
global.window = {dispatchEvent: function(e) { events.push(e); }};
global.CustomEvent = function(name, init) { this.type = name; this.detail = init.detail; };
var replaying = false;
var replayWindow = {active: function() { return replaying; }};
eval(announce + '\n    }');
announceText({role: 'assistant', bubble: {}, rawText: 'Good morning'}, 'm1');
check('a live update is not a replay', events.length === 1 && events[0].detail.replay === false && events[0].detail.text === 'Good morning');
replaying = true;
announceText({role: 'assistant', bubble: {}, rawText: 'Good morning'}, 'm1', {done: true});
check('a replayed one is', events.length === 2 && events[1].detail.replay === true && events[1].detail.done === true);
announceText({role: 'user', bubble: {}, rawText: 'Hi'}, 'u1');
check('a person\'s own message is not announced', events.length === 2);

process.exit(fail ? 1 : 0);
