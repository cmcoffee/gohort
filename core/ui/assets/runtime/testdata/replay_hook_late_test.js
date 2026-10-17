// An app's replay hook that registers after the thread was replayed still
// decorates what is on screen. A page's hook waits for the panel and then
// registers, and on a fast reload the thread came back first: its cards drew
// as plain bubbles while cards arriving later were decorated.
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

var msgEls = {}, messageReplayHooks = [];
eval(lift(panel, 'function registerReplayHook(fn)', 'registerReplayHook'));
eval(lift(panel, 'function runReplayHooks(mid, m)', 'runReplayHooks'));
check('the page hook is the late-aware one', panel.indexOf('window.uiRegisterMessageReplayHook = registerReplayHook;') >= 0);

// The thread replays before any hook exists.
msgEls.r1 = {bubble: {id: 'r1', cards: 0}};
runReplayHooks('r1', {id: 'r1', report_from: 'Daily news'});
msgEls.u1 = {bubble: {id: 'u1', cards: 0}};
runReplayHooks('u1', {id: 'u1', role: 'user', content: 'hi'});
msgEls.live = {bubble: {id: 'live', cards: 0}}; // drawn live, never replayed

var seen = [];
registerReplayHook(function(bubble, m) {
  seen.push(bubble.id);
  if (m.report_from) bubble.cards++;
});
check('a hook registered late decorates the replayed card', msgEls.r1.bubble.cards === 1);
check('it is handed every replayed message to decide on', seen.indexOf('u1') >= 0);
check('a bubble that was not replayed is left alone', seen.indexOf('live') < 0);

// And from then on it runs as messages are replayed, once each.
msgEls.r2 = {bubble: {id: 'r2', cards: 0}};
runReplayHooks('r2', {id: 'r2', report_from: 'Mail'});
check('a later card is decorated as it is drawn', msgEls.r2.bubble.cards === 1 && msgEls.r1.bubble.cards === 1);

if (fail) process.exit(1);
