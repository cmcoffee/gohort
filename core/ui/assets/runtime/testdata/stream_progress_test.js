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
  /uiChunkPacer\(function\(id, text\) \{\s*if \(thinkLive && text\.trim\(\)\) endThinkLive\(\);/.test(panel));
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
  /appendChunk\(id, text\);\s*if \(text\.trim\(\)\) writingNow\(\);/.test(panel) &&
  /replaceChunk\(ev\.id, ev\.text \|\| ''\);\s*writingNow\(\);/.test(panel) &&
  /noteThinking\(ev\);\s*waitingNow\(\);/.test(panel));
check('the turn ending clears the quiet timer',
  /clearTimeout\(thinkingQuietTimer\)/.test(lift(panel, 'function clearThinking(', 'clearThinking')));

// ---- markdown while streaming ----------------------------------------------

var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
var frames = [];
function requestAnimationFrame(fn) { frames.push(fn); return frames.length; }
function cancelAnimationFrame(id) { frames[id - 1] = null; }
function frame() { var fns = frames; frames = []; fns.forEach(function(f) { if (f) f(); }); }
var clock = 0;
var window = {uiStripMetaTags: function(s) { return s; }, performance: {now: function() { return clock; }}};
var performance = window.performance;
var renders = [];
// A node per rendered piece, holding the markdown it came from.
var document = {createElement: function() {
  return {childNodes: [], set innerHTML(v) { this.childNodes = [{src: v, parentNode: null}]; }, classList: {add: function() {}}};
}};
window.uiRenderMarkdown = function(body, text) { renders.push(text); body.innerHTML = text; };
function fakeBody() {
  var b = {kids: [], classList: {add: function() {}},
    appendChild: function(n) { n.parentNode = b; b.kids.push(n); },
    removeChild: function(n) { b.kids.splice(b.kids.indexOf(n), 1); n.parentNode = null; }};
  Object.defineProperty(b, 'innerHTML', {set: function() { b.kids.forEach(function(n) { n.parentNode = null; }); b.kids = []; }});
  return b;
}
function shown(b) { return b.kids.map(function(n) { return n.src; }).join(''); }
eval(lift(prelude, 'function uiStreamSettledCut(', 'uiStreamSettledCut'));
eval('var PACER_LAG = 0.3;');
eval(lift(prelude, 'window.uiChunkPacer = function(', 'uiChunkPacer'));
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

// The pacer: text comes out a few characters a frame, at the arrival rate.
var got = [];
var pacer = window.uiChunkPacer(function(id, text) { got.push(id + ':' + text); });
pacer.chunk('a', 'Good morning, this is a reply.');
check('nothing is delivered before a frame', got.length === 0);
clock += 16; frame();
check('the first text eases in, not all at once', got.length === 1 && got[0].length < 'a:Good morning, this is a reply.'.length);
pacer.flush();
check('flush delivers everything held', got.join('').replace(/a:/g, '') === 'Good morning, this is a reply.');
check('and asks for no more frames', frames.every(function(f) { return !f; }));

// A steady stream: about the arrival rate comes out per frame.
got = [];
var steady = window.uiChunkPacer(function(id, text) { got.push(text); });
for (var i = 0; i < 30; i++) { clock += 10; steady.chunk('s', 'abcd'); }
clock += 16; frame();
var step = got[got.length - 1].length;
check('a frame reveals about what arrives in one (400 chars/s, 16ms: ~6)', step >= 4 && step <= 30);
steady.flush();
check('nothing is lost or reordered', got.join('') === new Array(31).join('abcd'));

// An emoji is never split across two deliveries.
got = [];
var emo = window.uiChunkPacer(function(id, text) { got.push(text); });
emo.chunk('e', 'ab\uD83D\uDE00cd');
clock += 16; frame(); frame(); frame(); frame(); frame();
emo.flush();
check('no delivery ends between the halves of a surrogate pair',
  got.every(function(t) { var c = t.charCodeAt(t.length - 1); return !(c >= 0xD800 && c <= 0xDBFF); }));

// drop forgets what is held: a view switched to another thread.
got = [];
var dropped = window.uiChunkPacer(function(id, text) { got.push(text); });
dropped.chunk('d', 'stale text for a thread no longer on screen');
dropped.drop();
clock += 16; frame();
check('dropped text is never delivered', got.length === 0);

// The panel's own repaint is once per frame through the shared painter.
renders = [];
function unmarkEmptyBubble() {} function markEmptyBubble() {} function scrollConvo() {}
var cfg = {markdown: true};
eval(lift(panel, 'function streamingText(', 'streamingText'));
eval(lift(panel, 'function streamFrame(', 'streamFrame'));
eval(lift(panel, 'function showStreaming(', 'showStreaming'));
eval(lift(panel, 'function paintStreaming(', 'paintStreaming'));
eval(lift(panel, 'function paintStreamingNow(', 'paintStreamingNow'));
var removed = [];
var m = {role: 'assistant', rawText: '', body: fakeBody(),
  bubble: {classList: {remove: function(c) { removed.push(c); }}}};
function chunk(t) { m.rawText += t; showStreaming(m); }
frames = [];
chunk('# Title'); chunk('\n\nFirst');
check('chunks between frames wait for one frame', renders.length === 0 && frames.length === 1);
frame();
check('a frame paints what arrived meanwhile', shown(m.body) === '# Title\n\nFirst');
check('the raw-text pre-wrap comes off once markdown draws', removed.indexOf('ui-agent-msg-streaming') >= 0);
frame();
check('with nothing new no frame is asked for', frames.length === 0);

cfg.markdown = false;
var plain = {role: 'assistant', rawText: 'a\n\nb', body: {}, bubble: {classList: {remove: function() {}}}};
var rendered = renders.length;
showStreaming(plain);
frame();
check('an app with markdown off keeps plain text', plain.body.textContent === 'a\n\nb' && renders.length === rendered);

var finalize = lift(panel, 'function finalizeMessage(', 'finalizeMessage');
check('finishing cancels a pending repaint before the final render',
  finalize.indexOf('m.paintCancel()') >= 0 &&
  finalize.indexOf('m.paintCancel()') < finalize.indexOf('uiRenderMarkdown('));

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
