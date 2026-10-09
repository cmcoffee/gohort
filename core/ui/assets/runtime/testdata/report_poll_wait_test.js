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
var bgRunEl = null, bgRunId = '', bgRunTimer = null, serverClockOffset = null, convoStickToBottom = false;
var convoLog = {kids: [], appendChild: function(n) { this.kids.push(n); n.parentNode = this; },
  removeChild: function(n) { this.kids.splice(this.kids.indexOf(n), 1); n.parentNode = null; }};
function el(tag, attrs, kids) {
  return {tag: tag, attrs: attrs || {}, kids: [], textContent: (kids || []).join(''), parentNode: null,
    appendChild: function(c) { this.kids.push(c); c.parentNode = this; }, addEventListener: function(t, f) { this['on' + t] = f; }};
}
function keepPendingInterjectionsLast() {}
function scrollConvo() {}
function setInterval() { return 1; }
function clearInterval() {}
eval(lift(panel, 'function showBackgroundRun(bg)', 'showBackgroundRun'));
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
  // A run working for the thread in the background: shown with its label
  // and Stop, the poll tells the server which one it shows, and it goes
  // when the server stops reporting it.
  cfg.runs_url_base = 'api/runs/';
  timers = []; urls = [];
  answer = function() { now += 5; return Promise.resolve({Messages: [], background: {id: 'r7', label: 'Picking up a finished background task', started_ms: Date.now()}}); };
  startReportPolling('s1');
  await settle();
  var line = convoLog.kids[0];
  check('a background run is shown at the foot of the thread', line && /background task/.test(line.kids[1].textContent));
  check('with Stop', line && line.kids[2] && line.kids[2].textContent === 'Stop');
  check('a changed background run asks again at once', timers[0] === 0);
  urls = [];
  answer = function() { now += 25000; return Promise.resolve({Messages: []}); };
  startReportPolling('s1');
  await settle();
  check('the poll carries which run it shows', /&bg=/.test(urls[0]));
  check('a run no longer reported is taken away', convoLog.kids.length === 0);
  if (fail) process.exit(1);
})();
