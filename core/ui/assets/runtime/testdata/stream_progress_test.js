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
  var convoStickToBottom = true, scrolled = 0;
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

eval(lift(panel, 'function streamingText(', 'streamingText'));
eval(lift(panel, 'function streamingMarkdown(', 'streamingMarkdown'));

var fence = '```';
check('an open code fence is closed for the render',
  streamingMarkdown('Run this:\n' + fence + '\n# a comment') === 'Run this:\n' + fence + '\n# a comment\n' + fence);
check('a closed one is left alone',
  streamingMarkdown(fence + '\nx\n' + fence + '\nafter') === fence + '\nx\n' + fence + '\nafter');

// The real showStreaming / paintStreaming, against a fake clock.
var timers = [];
function setTimeout(fn) { timers.push(fn); return timers.length; }
function clearTimeout() {}
function fire() { var fns = timers; timers = []; fns.forEach(function(f) { f(); }); }
var renders = [];
var window = {uiStripMetaTags: function(s) { return s; }};
function uiRenderMarkdown(body, text) { renders.push(text); body.html = text; }
function unmarkEmptyBubble() {} function markEmptyBubble() {} function scrollConvo() {}
var cfg = {markdown: true};
var STREAM_PAINT_MS = 120;
eval(lift(panel, 'function showStreaming(', 'showStreaming'));
eval(lift(panel, 'function paintStreaming(', 'paintStreaming'));

var removed = [];
var m = {role: 'assistant', rawText: '', body: {},
  bubble: {classList: {remove: function(c) { removed.push(c); }}}};
function chunk(t) { m.rawText += t; showStreaming(m); }

chunk('# Title');
check('the first words render at once, as markdown', renders.length === 1 && renders[0] === '# Title');
check('the raw-text pre-wrap comes off once markdown draws', removed.indexOf('ui-agent-msg-streaming') >= 0);
chunk('\n\nFirst'); chunk(' paragraph.');
check('chunks inside the interval wait for it', renders.length === 1);
fire();
check('the interval paints what arrived meanwhile', renders.length === 2 && renders[1] === '# Title\n\nFirst paragraph.');
fire();
check('with nothing new it stops, no idle repaints', renders.length === 2 && timers.length === 0);

cfg.markdown = false;
var plain = {role: 'assistant', rawText: 'a\n\nb', body: {}, bubble: {classList: {remove: function() {}}}};
showStreaming(plain);
check('an app with markdown off keeps plain text', plain.body.textContent === 'a\n\nb' && renders.length === 2);

var finalize = lift(panel, 'function finalizeMessage(', 'finalizeMessage');
check('finishing cancels a pending repaint before the final render',
  finalize.indexOf('clearTimeout(m.paintTimer)') >= 0 &&
  finalize.indexOf('clearTimeout(m.paintTimer)') < finalize.indexOf('uiRenderMarkdown('));

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
