// A channel message the live poll appends is drawn as a reload draws it: the
// app's replay hooks run on its bubble (an app paints a delivered picture or
// song from one) and its tool calls come back as chips. Before, the poll drew
// the text alone, and a reply's attachment showed only after a reload.
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

var cfg = {markdown: false};
var msgEls = {}, added = [], hooked = [], toolsApplied = [];
function addMessage(role, mid, text) { added.push(mid); msgEls[mid] = {bubble: {id: mid}}; }
function finalizeMessage() {}
function setMessageMeta() {}
function applyPersistedToolCalls(mid, m) { if (m.tool_calls) toolsApplied.push(mid); }
var messageReplayHooks = [function(bubble, m) { hooked.push(bubble.id + ':' + (m.files || []).length); }];
var arrived = [];
var window = {dispatchEvent: function(e) { arrived.push(e); }};
global.CustomEvent = function(type, init) { this.type = type; this.detail = init && init.detail; };
eval(lift(panel, 'function announceArrived(mid, m)', 'announceArrived'));
eval(lift(panel, 'function runReplayHooks(mid, m)', 'runReplayHooks'));
eval(lift(panel, 'function renderObservation(m)', 'renderObservation'));
eval(lift(panel, 'function appendChannelMessage(m)', 'appendChannelMessage'));

appendChannelMessage({id: 'm1', role: 'assistant', content: 'here is the song',
  files: [{id: 'f1', kind: 'audio', name: 'song.mp3'}], tool_calls: [{name: 'make_song'}]});
check('the message is drawn', added[0] === 'm1');
check('the replay hooks run on its bubble', hooked[0] === 'm1:1');
check('its tool calls are drawn', toolsApplied[0] === 'm1');

appendChannelMessage({id: 'm2', role: 'user', content: 'hidden', hidden: true});
check('a hidden message is not drawn', added.length === 1 && hooked.length === 1);

// A message arriving live is announced to page scripts with its bubble, so
// one can act on it as it comes (a result read aloud). Drawing alone is not
// an arrival: a reload's replay draws the whole thread, and acting on each
// message again there would read out the history.
check('a live arrival is announced with the message and its bubble', arrived.length === 1 &&
  arrived[0].type === 'ui-agent-message-arrived' && arrived[0].detail.message.id === 'm1' && arrived[0].detail.bubble === msgEls.m1.bubble);
renderObservation({id: 'm3', role: 'assistant', content: 'from history'});
check('drawing a message is not announcing it', arrived.length === 1);
check('the report poll announces what it draws', /announceArrived\(renderObservation\(m\), m\);\s*drew\+\+;/.test(panel));

if (fail) process.exit(1);
