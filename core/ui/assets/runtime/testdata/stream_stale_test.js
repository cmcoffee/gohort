// A stream the panel has let go of draws nothing and is closed.
//
// A page the browser kept for Back came back with its run stream still in it,
// and the browser reconnected that stream on its own, from the since it was
// opened with, every three seconds: each time it replayed a finished turn
// over the conversation, so every new reply read as the one before it, until
// a reload. A stream that is not the panel's current one is closed the moment
// it speaks, and the panel closes its stream when the page is put away.
//
// Drives the real subscribe functions, lifted from the runtime source, with
// EventSource and the rest of the panel replaced by stand-ins.
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

var opened = [];
function EventSource(url) {
  this.url = url; this.closed = false; this.readyState = 0;
  opened.push(this);
}
EventSource.CLOSED = 2;
EventSource.prototype.close = function() { this.closed = true; this.readyState = 2; };

var cfg = {runs_url_base: 'api/runs/', events_url: 'api/events'};
var activeEventSource = null, activeSessionId = 's1', sawTurnEnd = false;
var handled = [], lost = 0, idle = 0;
var replayWindow = {arm: function() {}};
function showWaitingNotes() {}
function handleEvent(ev) { handled.push(ev.kind); }
function streamLost() { lost++; }
function enableInput() { idle++; }
function openSession() {}
function addActivity() {}
var eventsSeen = 0, eventsRetry = 0;

eval(lift(panel, 'function subscribeRunStream(runId, since)', 'subscribeRunStream'));
eval(lift(panel, 'function subscribeEvents(sid, skip)', 'subscribeEvents'));

// The current stream is drawn.
subscribeRunStream('r1', 0);
var first = opened[0];
first.onmessage({data: '{"kind":"chunk"}'});
check('the current stream is drawn', handled.join() === 'chunk');

// Let go of (a newer turn's stream, or the page put away): it draws nothing,
// and is closed so the browser stops reconnecting it.
subscribeRunStream('r2', 0);
check('a new subscription closes the one before', first.closed);
first.closed = false; // as a browser restoring a cached page would leave it
handled = [];
first.onmessage({data: '{"kind":"chunk"}'});
check('a stream let go of draws nothing', handled.length === 0);
check('and is closed when it speaks', first.closed);
first.closed = false;
var lostBefore = lost;
first.onerror();
check('its error closes it and starts no rejoin', first.closed && lost === lostBefore);

// The current one still rejoins when it drops.
opened[1].onerror();
check('the current stream dropping still rejoins', lost === lostBefore + 1);

// The session events stream follows the same rule.
activeEventSource = null; opened = []; handled = [];
subscribeEvents('s1', 0);
var ev1 = opened[0];
activeEventSource = null;
ev1.onmessage({data: '{"kind":"chunk"}'});
check('a session stream let go of draws nothing and is closed', handled.length === 0 && ev1.closed);

// Put away for Back: the stream is closed; brought back, the panel rejoins.
check('a page put away for Back closes its stream',
  /addEventListener\('pagehide', function\(e\) \{ if \(e\.persisted\) detachActiveStream\(\); \}\)/.test(panel));

if (fail) process.exit(1);
