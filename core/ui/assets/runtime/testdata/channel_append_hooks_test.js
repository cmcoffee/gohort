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

if (fail) process.exit(1);
