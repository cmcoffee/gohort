// The page's change feed: one long poll on /api/changes, answered when work
// starts or ends or the viewer's sessions change. Run as shipped, over a
// stand-in fetch: the first answer is only a baseline, a moved topic is said
// once, live refreshes the pill, and a hidden tab lets go of its request.
var fs = require('fs');
var epi = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');
var a = epi.indexOf('      var feedSeen = ');
var b = epi.indexOf('      // The backstop: the poll it replaced');
if (a < 0 || b < a) { console.log('FAIL the change feed is gone'); process.exit(1); }

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var asked = [], heard = [], lives = 0, listeners = {}, aborted = 0;
function AbortController() { var sig = {}; this.signal = sig; this.abort = function() { aborted++; if (sig.onabort) sig.onabort(); }; }
var pending = null;
function fetch(url, o) {
  asked.push(url);
  return new Promise(function(resolve, reject) {
    pending = {resolve: function(v) { resolve({ok: true, json: function() { return v; }}); }, reject: reject, signal: o.signal};
    if (o.signal) o.signal.onabort = function() { var e = new Error('aborted'); e.name = 'AbortError'; reject(e); };
  });
}
var document = {hidden: false, addEventListener: function(n, f) { listeners[n] = f; }};
var window = {AbortController: AbortController, dispatchEvent: function(e) { heard.push(e.detail.topics.join()); }};
function CustomEvent(n, o) { this.detail = o.detail; }
function refreshLive() { lives++; }
var timers = [];
function setTimeout(f, ms) { timers.push({f: f, ms: ms}); }
function tick() { var t = timers.shift(); if (t) t.f(); }
function answer(v) { var p = pending; pending = null; p.resolve(v); }
function settle(next) { setImmediate(function() { setImmediate(next); }); }

eval(epi.slice(a, b));

check('it asks with nothing seen, waiting', asked.length === 1 && /wait=1&live=&sessions=$/.test(asked[0]));
answer({live: 'e.1', sessions: 'e.1'});
settle(function() {
  check('the first answer is a baseline: nothing said', heard.length === 0 && lives === 0);
  tick();
  check('it asks again with what it saw', asked.length === 2 && /live=e\.1&sessions=e\.1$/.test(asked[1]));
  answer({live: 'e.1', sessions: 'e.2'});
  settle(function() {
    check('a moved topic is said once: ' + heard.join(' | '), heard.length === 1 && heard[0] === 'sessions' && lives === 0);
    tick();
    answer({live: 'e.2', sessions: 'e.2'});
    settle(function() {
      check('live moving refreshes the pill', lives === 1 && heard[1] === 'live');
      tick();
      var before = asked.length;
      document.hidden = true;
      listeners.visibilitychange();
      settle(function() {
        tick(); // the retry after the abort
        check('a hidden tab lets go of its request and does not ask again', aborted === 1 && asked.length === before);
        document.hidden = false;
        listeners.visibilitychange();
        check('shown again, it asks at once with what it saw', asked.length === before + 1 && /live=e\.2&sessions=e\.2$/.test(asked[asked.length - 1]));
        process.exit(fail ? 1 : 0);
      });
    });
  });
});
