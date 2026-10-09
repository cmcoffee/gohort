// A reply in progress: the live thinking line, markdown while streaming, and
// the stats footer that explains its own figures.
//
// Drives the real functions, lifted from the runtime source, with the DOM and
// the clock replaced by stand-ins.
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
  var depth = 0, j = src.indexOf(start.indexOf('[') >= 0 ? '[' : '{', i);
  var open = src[j], close = open === '[' ? ']' : '}';
  for (; j < src.length; j++) {
    if (src[j] === open) depth++;
    else if (src[j] === close && --depth === 0) break;
  }
  return src.slice(i, j + 1) + (open === '[' ? ';' : '');
}

var basics = fs.readFileSync(dir + '/10_basics.js', 'utf8');
var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');
var chat = fs.readFileSync(dir + '/20_chat_panel.js', 'utf8');

// ---- the live thinking line ------------------------------------------------

eval(lift(basics, 'function statsSeconds(', 'statsSeconds'));
eval(lift(basics, 'function thinkingLabel(', 'thinkingLabel'));

check('thinking with nothing counted yet says only that',
  thinkingLabel(400, 0) === 'Thinking');
check('seconds and an approximate token count once there are some',
  thinkingLabel(34200, 1200) === 'Thinking · 34s · ~1,200 tokens');
check('past a minute it reads as minutes',
  thinkingLabel(65000, 0) === 'Thinking · 1m 05s');

// The server reports it, the panel shows it, and anything the model produces
// ends it.
check('a thinking event updates the line',
  /case 'thinking':\s*noteThinking\(ev\);/.test(panel));
check('the line sits beside the dots',
  /ui-agent-thinking-label/.test(lift(panel, 'function showThinking(', 'showThinking')));
check('visible text ends it',
  /case 'chunk':\s*if \(thinkLive && \(ev\.text \|\| ''\)\.trim\(\)\) endThinkLive\(\);/.test(panel));
check('a tool call ends it',
  /case 'tool_call': \{\s*endThinkLive\(\);/.test(panel));
check('the turn ending ends it, timer included',
  /endThinkLive\(\)/.test(lift(panel, 'function clearThinking(', 'clearThinking')) &&
  /clearInterval\(thinkLiveTimer\)/.test(lift(panel, 'function endThinkLive(', 'endThinkLive')));
check('the seconds advance between server ticks',
  /setInterval\(renderThinkLive, 1000\)/.test(lift(panel, 'function noteThinking(', 'noteThinking')));

// ---- the dots step aside while the reply is written --------------------------

// The dots say "waiting": hidden while words arrive, back once the writing has
// been quiet for a moment, or at once when the model thinks again. Driven with
// a stand-in element and clock.
(function() {
  var timers = [], now = 0;
  var setTimeout = function(fn, ms) { timers.push({fn: fn, at: now + ms}); return timers.length; };
  var clearTimeout = function(id) { if (id && timers[id - 1]) timers[id - 1].fn = null; };
  function advance(ms) {
    now += ms;
    timers.forEach(function(t) { if (t.fn && t.at <= now) { var f = t.fn; t.fn = null; f(); } });
  }
  var thinkingEl = {style: {display: ''}};
  var thinkingQuietTimer = null, thinkingQuietMs = 1500;
  var convoStickToBottom = true, scrolled = 0, convoLog = null;
  function keepPendingInterjectionsLast() {}
  function scrollConvo() { scrolled++; }
  eval(lift(panel, 'function writingNow(', 'writingNow'));
  eval(lift(panel, 'function waitingNow(', 'waitingNow'));

  writingNow();
  check('the dots hide while words arrive', thinkingEl.style.display === 'none');
  advance(800); writingNow(); advance(1000);
  check('a steady stream keeps them hidden', thinkingEl.style.display === 'none');
  advance(600);
  check('they come back once the writing has gone quiet', thinkingEl.style.display === '' && scrolled === 1);
  writingNow(); waitingNow();
  check('thinking again brings them back at once', thinkingEl.style.display === '');
  thinkingEl = null;
  writingNow(); waitingNow();
  check('with no dots on screen there is nothing to hide or show', thinkingEl === null);
})();
check('visible text and a replaced chunk hide them; a thinking event shows them',
  /appendChunk\(ev\.id, ev\.text \|\| ''\);\s*if \(\(ev\.text \|\| ''\)\.trim\(\)\) writingNow\(\);/.test(panel) &&
  /replaceChunk\(ev\.id, ev\.text \|\| ''\);\s*writingNow\(\);/.test(panel) &&
  /noteThinking\(ev\);\s*waitingNow\(\);/.test(panel));
check('the turn ending clears the quiet timer',
  /clearTimeout\(thinkingQuietTimer\)/.test(lift(panel, 'function clearThinking(', 'clearThinking')));

// ---- markdown while streaming ----------------------------------------------

var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
var frames = [], timers = [];
function requestAnimationFrame(fn) { frames.push(fn); return frames.length; }
function cancelAnimationFrame(id) { frames[id - 1] = null; }
function frame() { var fns = frames; frames = []; fns.forEach(function(f) { if (f) f(); }); }
function setTimeout(fn) { timers.push(fn); return timers.length; }
function clearTimeout(id) { timers[id - 1] = null; }
var clock = 0;
var window = {uiStripMetaTags: function(s) { return s; }, performance: {now: function() { return clock; }}};
var performance = window.performance;
var renders = [];
// A node per rendered piece, holding the markdown it came from.
var document = {hidden: false, createElement: function() {
  return {childNodes: [], set innerHTML(v) { this.childNodes = [{src: v, parentNode: null}]; }, classList: {add: function() {}}};
}};
window.uiRenderMarkdown = function(body, text) { renders.push(text); body.innerHTML = text; };
function fakeBody() {
  var b = {kids: [], classList: {add: function() {}},
    appendChild: function(n) { n.parentNode = b; b.kids.push(n); },
    removeChild: function(n) { b.kids.splice(b.kids.indexOf(n), 1); n.parentNode = null; }};
  Object.defineProperty(b, 'innerHTML', {set: function(v) {
    b.kids.forEach(function(n) { n.parentNode = null; }); b.kids = v ? [{src: v, parentNode: b}] : []; }});
  return b;
}
function shown(b) { return b.kids.map(function(n) { return n.src; }).join(''); }
eval(lift(prelude, 'function uiStreamSettledCut(', 'uiStreamSettledCut'));
eval('var PACER_LAG = 0.3, PACER_FLOOR = 0.7;');
eval(lift(prelude, 'window.uiStreamReveal = function(', 'uiStreamReveal'));
eval(lift(prelude, 'window.uiReplayWindow = function(', 'uiReplayWindow'));
eval(lift(prelude, 'window.uiStreamMarkdown = function(', 'uiStreamMarkdown'));

// The painter: only the block still being written renders again.
var pb = fakeBody(), paint = window.uiStreamMarkdown(pb);
paint('# Title');
check('the first words render as markdown', shown(pb) === '# Title');
paint('# Title\n\nFirst paragraph.');
var before = renders.length;
paint('# Title\n\nFirst paragraph. More.');
check('a finished block is not rendered again', renders.length === before + 1 && renders[renders.length - 1] === 'First paragraph. More.');
check('the whole reply is on screen in order', shown(pb) === '# Title\n\nFirst paragraph. More.');
var fence = '```';
paint('# Title\n\nRun:\n' + fence + '\n# a comment');
check('an open code fence is closed for the render', renders[renders.length - 1] === 'Run:\n' + fence + '\n# a comment\n' + fence);
paint('Replaced.');
check('text that changed rather than grew starts over', shown(pb) === 'Replaced.');
check('a cut never lands inside a code fence', uiStreamSettledCut('a\n\n```\nx\n\ny\n') === 3);
check('nor before a list item that may belong above', uiStreamSettledCut('- a\n\n- b') === 0);
check('it lands after a blank line before a paragraph', uiStreamSettledCut('p1\n\np2') === 4);

// The revealer: display only. It paints a growing prefix of text the caller
// keeps whole; it holds no events and loses nothing.
var painted = '';
var rv = window.uiStreamReveal(function(p) { painted = p; });
var full = '';
for (var i = 0; i < 30; i++) { clock += 10; full += 'abcd'; rv.update(full); }
check('nothing is painted before a frame', painted === '');
clock += 16; frame();
check('a frame paints part of it, not all', painted.length > 0 && painted.length < full.length);
check('what is painted is the start of the text', full.indexOf(painted) === 0);
for (var f = 0; f < 200 && painted.length < full.length; f++) { clock += 16; frame(); }
check('left alone it reaches all of the text', painted === full);
check('and then asks for no more frames', frames.every(function(x) { return !x; }));

rv.update(full + ' more');
rv.finish();
check('finish paints all of it at once', painted === full + ' more');

rv.update('A correction.');
check('text that changed rather than grew shows as it now is, at once', painted === 'A correction.');

// The end of a reply drains the reserve like a pause does. It eases off, but
// it must not crawl: at a 0.2 floor the last line typed at a fifth the speed.
var lens = [];
var tail = window.uiStreamReveal(function(p) { lens.push(p.length); });
var steady = '';
for (var s2 = 0; s2 < 80; s2++) { clock += 16; steady += 'abcdefg'; tail.update(steady); frame(); }
var streamed = lens.length;
for (var d2 = 0; d2 < 200 && lens[lens.length - 1] < steady.length; d2++) { clock += 16; frame(); }
var steps = [];
for (var k = streamed; k < lens.length - 1; k++) steps.push(lens[k] - lens[k - 1]);
var steadyStep = (lens[streamed - 1] - lens[streamed - 21]) / 20;
check('the reserve drains to the end once the text stops', lens[lens.length - 1] === steady.length);
check('the end slows by at most a third, never to a crawl',
  steps.length > 0 && steps.every(function(x) { return x >= steadyStep * 0.6; }));

var cuts = [];
var emo = window.uiStreamReveal(function(p) { cuts.push(p); });
emo.update('ab\uD83D\uDE00cdefghijklmnop');
for (var e = 0; e < 20; e++) { clock += 16; frame(); }
check('no paint ends between the halves of a surrogate pair',
  cuts.every(function(t) { var c = t.charCodeAt(t.length - 1); return !(c >= 0xD800 && c <= 0xDBFF); }));

// A hidden tab gets no display frames; the reveal keeps up on a timer.
document.hidden = true;
painted = '';
var hid = window.uiStreamReveal(function(p) { painted = p; });
hid.update('written while the tab was hidden');
check('a hidden tab reveals on a timer, not a frame', timers.length > 0 && frames.length === 0);
document.hidden = false;

// A rejoin replays what already streamed: shown whole, not typed out again.
var rw = window.uiReplayWindow(400);
rw.arm();
clock += 2000; // connecting took a while: the window opens at the first arrival
rw.seen();
painted = '';
var rj = window.uiStreamReveal(function(p) { painted = p; }, {instant: rw.active});
rj.update('everything that streamed before the refresh.');
check('a replay shows whole at once', painted === 'everything that streamed before the refresh.');
clock += 500;
rj.update('everything that streamed before the refresh. Then live text.');
check('live text after the window reveals evenly again', painted === 'everything that streamed before the refresh.');
rj.finish();

// The panel: the text and every event stay current; only the paint lags.
renders = [];
frames = [];
function unmarkEmptyBubble() {} function markEmptyBubble() {} function scrollConvo() {}
var cfg = {markdown: true};
var replayWindow = window.uiReplayWindow(400);
var msgEls = {};
eval(lift(panel, 'function streamingText(', 'streamingText'));
eval(lift(panel, 'function revealFor(', 'revealFor'));
eval(lift(panel, 'function showStreaming(', 'showStreaming'));
eval(lift(panel, 'function showVisible(', 'showVisible'));
eval(lift(panel, 'function appendChunk(', 'appendChunk'));
eval(lift(panel, 'function replaceChunk(', 'replaceChunk'));
var removed = [];
function addMessage(role, id) {
  return (msgEls[id] = {role: role, rawText: '', body: fakeBody(),
    bubble: {classList: {remove: function(c) { removed.push(c); }}}});
}
appendChunk('m1', '# Title'); appendChunk('m1', '\n\nFirst');
check('the text is current the moment a chunk arrives', msgEls.m1.rawText === '# Title\n\nFirst');
for (var g = 0; g < 50; g++) { clock += 16; frame(); }
check('the frames paint it, as markdown', shown(msgEls.m1.body) === '# Title\n\nFirst');
check('the raw-text pre-wrap comes off once markdown draws', removed.indexOf('ui-agent-msg-streaming') >= 0);
replaceChunk('m1', 'Cleaned.');
check('a replace shows at once', shown(msgEls.m1.body) === 'Cleaned.');

cfg.markdown = false;
appendChunk('m2', 'a\n\nb');
for (var h = 0; h < 50; h++) { clock += 16; frame(); }
check('an app with markdown off keeps plain text', msgEls.m2.body.textContent === 'a\n\nb');

var finalize = lift(panel, 'function finalizeMessage(', 'finalizeMessage');
check('finishing paints all of the text before the final render',
  finalize.indexOf('m.reveal.finish()') >= 0 &&
  finalize.indexOf('m.reveal.finish()') < finalize.indexOf('uiRenderMarkdown('));

// ---- the stats footer ------------------------------------------------------

function el(tag, attrs, kids) { return {tag: tag, attrs: attrs || {}, kids: kids || []}; }
eval(lift(basics, 'var STATS_FIELDS = [', 'STATS_FIELDS'));
eval(lift(basics, 'function statsFooterNodes(', 'statsFooterNodes'));

var nodes = statsFooterNodes({tokens_per_sec: 41.23, elapsed_ms: 18700, think_ms: 41000,
  input_tokens: 12345, output_tokens: 678, reasoning_tokens: 0});
var text = nodes.map(function(n) { return typeof n === 'string' ? n : n.kids.join(''); }).join('');
check('the line reads as before, with thinking time added',
  text === '41.2 tk/s - 18.7s - thought 41.0s - 12,345 in - 678 out');
check('every figure says what it measures',
  nodes.filter(function(n) { return typeof n !== 'string'; })
       .every(function(n) { return n.tag === 'span' && n.attrs.title && n.attrs.title.length > 20; }));
check('tk/s says it leaves prompt processing out',
  /Prompt processing is not included/.test(nodes[0].attrs.title));
check('no figures, no line', statsFooterNodes({}).length === 0 && statsFooterNodes(null).length === 0);

check('the agent panel builds its footer with it',
  /statsFooterNodes\(ev\)/.test(lift(panel, 'function renderMessageStats(', 'renderMessageStats')));
check('the chat panel builds its footer with it',
  /statsFooterNodes\(stats\)/.test(lift(chat, 'function renderRoundStats(', 'renderRoundStats')));
check('a reloaded message keeps its thinking time',
  /think_ms:\s*meta\.usage\.think_ms/.test(panel));

process.exit(fail ? 1 : 0);
