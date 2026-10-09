// A stream that replays from the start on every connect is reconnected by
// the panel itself, past the events the view already has, instead of by the
// browser, whose reconnect drew the whole session a second time.
//
// Drives the real subscribeEvents, lifted from the runtime source, over a
// stand-in EventSource and clock.
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
function EventSource(url) { this.url = url; this.readyState = 0; this.closed = false; opened.push(this); }
EventSource.CLOSED = 2;
EventSource.prototype.close = function() { this.closed = true; this.readyState = 2; };
var later = [];
function setTimeout(fn) { later.push(fn); }
function runLater() { var l = later; later = []; l.forEach(function(f) { f(); }); }

var cfg = {events_url: 'api/events'};
var activeSessionId = 's1', activeEventSource = null, sawTurnEnd = false, eventsSeen = 0, eventsRetry = 0;
var handled = [], log = [];
function handleEvent(ev) { handled.push(ev.n); if (ev.done) sawTurnEnd = true; }
function enableInput() { log.push('idle'); }
function openSession(sid) { log.push('reload ' + sid); }
function addActivity(k, id, t) { log.push('note'); }
var chunkPacer = {flush: function() {}}; // nothing held: handleEvent here applies at once
eval(lift(panel, 'function subscribeEvents(sid, skip)', 'subscribeEvents'));
function send(es, n, extra) { es.onmessage({data: JSON.stringify(Object.assign({n: n}, extra || {}))}); }

subscribeEvents('s1');
var a = opened[0];
send(a, 1); send(a, 2); send(a, 3);
a.onerror(); // dropped: the browser would now reconnect and replay 1..3 again
check('the panel closes the dropped stream rather than let the browser reconnect it', a.closed);
runLater();
var b = opened[1];
check('and opens its own', !!b && activeEventSource === b);
send(b, 1); send(b, 2); send(b, 3); send(b, 4);
check('the replay of what the view has is skipped, the rest is drawn: ' + handled.join(','), handled.join(',') === '1,2,3,4');

send(b, 5, {done: true});
b.readyState = 2;
b.onerror();
check('a stream that ended with its turn just goes idle', log.join(',') === 'idle' && opened.length === 2);

log = []; handled = [];
subscribeEvents('s2');
var c = opened[2];
c.readyState = 2;
activeSessionId = 's2';
c.onerror();
check('a stream the server refused outright goes idle, no retrying', log.join(',') === 'idle' && later.length === 0);

log = [];
subscribeEvents('s2', 4);
var d = opened[3];
d.readyState = 2;
d.onerror();
check('refused on a reconnect: it ended while away, so the thread reloads', log.join(',') === 'reload s2');

if (fail) process.exit(1);
