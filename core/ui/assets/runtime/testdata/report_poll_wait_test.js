// The card poll waits on the server (wait=1) and asks again at once when the
// server held it; a server that answers at once with nothing sends it back to
// the six-second tick, so it never spins.
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

var window = {AbortController: AbortController};
var cfg = {load_url: 'api/sessions/{id}'}, activeSessionId = 's1', msgsF = 'Messages';
var cortexObsSince = '', cortexObsSeen = {}, channelPollTimer = null, reportPollGen = 0, reportPollAbort = null;
var urls = [], timers = [], drawn = [], now = 0, answer = null;
var Date = {now: function() { return now; }};
function setTimeout(fn, ms) { timers.push(ms); }
function substituteExtras(u) { return u; }
function noteObsSince() {}
function obsKey(m) { return m.id; }
function renderObservation(m) { drawn.push(m.id); }
function fetchJSON(url) { urls.push(url); return answer(); }
eval(lift(panel, 'function stopChannelPolling()', 'stopChannelPolling'));
eval(lift(panel, 'function startReportPolling(sid)', 'startReportPolling'));
function settle() { return new Promise(function(r) { global.setTimeout(r, 0); }); }

(async function() {
  answer = function() { now += 25000; return Promise.resolve({Messages: []}); };
  startReportPolling('s1');
  await settle();
  check('the poll asks the server to wait', /cards=1&wait=1/.test(urls[0]));
  check('a held answer is asked again at once', timers[0] === 0);

  timers = [];
  answer = function() { now += 5; return Promise.resolve({Messages: []}); };
  startReportPolling('s1');
  await settle();
  check('an immediate empty answer falls back to the six-second tick', timers[0] === 6000);

  timers = [];
  answer = function() { now += 5; return Promise.resolve({Messages: [{id: 'c1', report_from: 'task'}]}); };
  startReportPolling('s1');
  await settle();
  check('an immediate answer with a card asks again at once', drawn[0] === 'c1' && timers[0] === 0);
  if (fail) process.exit(1);
})();
