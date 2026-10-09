// A turn whose stream drops is rejoined, not abandoned: still running, it is
// subscribed again from what this view already has; finished meanwhile, the
// thread reloads for its ending. Coming back to the page does the same. A
// replayed thinking tick is timed from when its span began on the server.
//
// Drives the real functions, lifted from the runtime source, with the
// network, clock and the rest of the panel replaced by stand-ins.
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

var cfg = {runs_url_base: 'api/runs/'};
var activeSessionId = 's1', activeRunId = 'r1', activeStream = null, activeEventSource = null;
var runSeqReceived = 40, turnLive = true, recheckInFlight = false, serverClockOffset = null;
var log = [], answer = null, later = [];
function setTimeout(fn) { later.push(fn); }
function detachActiveStream() { log.push('detach'); activeStream = null; activeEventSource = null; }
function enableInput() { log.push('idle'); turnLive = false; }
function disableInput() { log.push('busy'); turnLive = true; }
function subscribeRunStream(id, since) { log.push('subscribe ' + id + ' since ' + since); activeEventSource = {}; }
function openSession(sid) { log.push('reload ' + sid); }
function addActivity(k, id, t) { log.push('note ' + t); }
function fetchJSON(url) { log.push('probe'); return answer(); }
function tick() { return new Promise(function(r) { global.setTimeout(r, 0); }); }

eval(lift(panel, 'function streamLost()', 'streamLost'));
eval(lift(panel, 'function recheckRun(sid, attempt)', 'recheckRun'));
eval(lift(panel, 'function noteServerClock(d)', 'noteServerClock'));
eval(lift(panel, 'function backOnPage(long)', 'backOnPage'));
var convoLog = {isConnected: true};

(async function() {
  answer = function() { return Promise.resolve({run_id: 'r1', now_ms: Date.now() - 2000}); };
  streamLost();
  await tick();
  check('a dropped stream of a running turn is rejoined from what the view has: ' + log.join(', '),
    log.join(',') === 'detach,probe,busy,subscribe r1 since 40');
  check('the probe sets the server clock', Math.abs(serverClockOffset - 2000) < 50);

  log = []; activeEventSource = null; turnLive = true;
  answer = function() { return Promise.resolve({now_ms: Date.now()}); };
  streamLost();
  await tick();
  check('a turn that finished meanwhile reloads the thread for its ending: ' + log.join(', '),
    log.join(',') === 'detach,probe,reload s1');

  log = []; turnLive = true;
  answer = function() { return Promise.reject(new Error('offline')); };
  streamLost();
  await tick();
  check('offline, it tries again rather than going idle', later.length === 1 && log.indexOf('idle') < 0);

  log = []; later = []; activeSessionId = 's2';
  answer = function() { return Promise.resolve({run_id: 'r9'}); };
  recheckRun('s1', 0);
  await tick();
  check('an answer for a session no longer open changes nothing', log.join(',') === 'probe');
  activeSessionId = 's1';

  log = []; turnLive = true; activeEventSource = {};
  answer = function() { return Promise.resolve({run_id: 'r1'}); };
  backOnPage(true);
  await tick();
  check('back after a while with a turn running: the stream is replaced from where it was: ' + log.join(', '),
    log.join(',') === 'detach,probe,busy,subscribe r1 since 40');

  log = []; turnLive = false; activeEventSource = null;
  answer = function() { return Promise.resolve({run_id: 'r2'}); };
  backOnPage(false);
  await tick();
  check('back on an idle view, a turn started elsewhere is joined', log.join(',') === 'probe,busy,subscribe r2 since 40');

  log = []; convoLog.isConnected = false;
  backOnPage(true);
  check('a panel no longer on the page does nothing', log.length === 0);

  // The thinking line on a replayed tick.
  var thinkLive = null, thinkLiveTimer = 1, thinkingEl = {};
  function renderThinkLive() {}
  function showThinking() {}
  eval(lift(panel, 'function noteThinking(ev)', 'noteThinking'));
  serverClockOffset = 0;
  var began = Date.now() - 60000;
  noteThinking({elapsed_ms: 3000, started_ms: began, tokens: 900});
  check('a replayed tick is timed from when its span began, not from the replay', Math.abs(thinkLive.since - began) < 50);
  serverClockOffset = null;
  noteThinking({elapsed_ms: 3000, started_ms: began});
  check('with no server clock it falls back to elapsed_ms', Math.abs(Date.now() - 3000 - thinkLive.since) < 50);

  check('the run event starts the count at the run\'s own 2', /activeRunId = ev\.id \|\| '';[\s\S]{0,400}runSeqReceived = 2;/.test(panel));
  check('the run stream never leaves reconnecting to the browser',
    /es\.onerror = function\(\) \{[\s\S]*?es\.close\(\);[\s\S]*?streamLost\(\);/.test(panel));
  check('a send stream that ends before its turn rejoins',
    /if \(r\.done\) \{\s*if \(activeRunId && !sawTurnEnd && cfg\.runs_url_base\) streamLost\(\);/.test(panel));
  check('a resume answer for another session is dropped',
    /if \(sid !== activeSessionId\) return; \/\/ switched away while asking/.test(panel));
  if (fail) process.exit(1);
})();
