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
var observers = [];
var window = {dispatchEvent: function(e) { arrived.push(e); },
  ResizeObserver: function(fn) { var o = {fn: fn, gone: false, observe: function(n) { o.node = n; }, disconnect: function() { o.gone = true; }}; observers.push(o); return o; }};
global.CustomEvent = function(type, init) { this.type = type; this.detail = init && init.detail; };
// The thread: a pane 500 high, scrolled to the bottom of 2000 of content.
var convoLog = {scrollTop: 1500, scrollHeight: 2000, clientHeight: 500, kids: [],
  getBoundingClientRect: function() { return {top: 0}; },
  appendChild: function(n) { var i = this.kids.indexOf(n); if (i >= 0) this.kids.splice(i, 1); this.kids.push(n); n.parentNode = this; },
  removeChild: function(n) { this.kids.splice(this.kids.indexOf(n), 1); n.parentNode = null; }};
var convoStickToBottom = true, convoReaderLeft = false, scrolledToEnd = 0;
function scrollConvo(force) { if (force || convoStickToBottom) { convoLog.scrollTop = convoLog.scrollHeight; scrolledToEnd++; } }
function el(tag, attrs, kids) {
  return {tag: tag, attrs: attrs, textContent: (kids || []).join(''), parentNode: null, on: {},
    classList: {contains: function(c) { return (attrs['class'] || '') === c; }},
    addEventListener: function(t, f) { this.on[t] = f; }};
}
eval(lift(panel, 'function announceArrived(mid, m)', 'announceArrived'));
eval(/var arrivalFollowMs = \d+;/.exec(panel)[0]);
eval(lift(panel, 'function followArrival(bubble)', 'followArrival'));
eval(lift(panel, 'function placeArrival(bubble)', 'placeArrival'));
var newBelow = null;
eval(lift(panel, 'function showNewBelow(bubble)', 'showNewBelow'));
eval(lift(panel, 'function hideNewBelow()', 'hideNewBelow'));
// A card's top as the pane sees it.
function card(top) { return {parentNode: convoLog, r: {top: top}, getBoundingClientRect: function() { return this.r; }}; }
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
check('and an arrival is followed as it settles', observers.length === 1 && observers[0].node === msgEls.m1.bubble);
check('the report poll announces what it draws', /announceArrived\(renderObservation\(m\), m\);\s*drew\+\+;/.test(panel));

// Followed as it settles: a reader at the bottom is taken to what arrived,
// all of it when it fits, else its top, as its picture loads.
// At the bottom of 2100, a 100-high arrival at the pane's 400.
var short = card(400);
convoLog.scrollTop = 1600; convoLog.scrollHeight = 2100;
followArrival(short);
check('one that fits is shown whole, at the bottom', convoLog.scrollTop === 2100);
var ro = observers[observers.length - 1];
check('and watched as it grows', ro.node === short);
// Its picture decoded: 900 high, taller than the pane.
convoLog.scrollTop = 1600; convoLog.scrollHeight = 2900;
ro.fn();
check('grown taller than the pane, its top is put at the top: the words first', convoLog.scrollTop === 1600 + 400 - 8);
convoReaderLeft = true;
var before = convoLog.scrollTop;
ro.fn();
check('a reader who scrolls up is left where they are', convoLog.scrollTop === before && ro.gone);
convoReaderLeft = false;

// Further up the thread: not moved, told instead.
convoStickToBottom = false;
convoLog.scrollTop = 200;
var later = card(1800);
followArrival(later);
var pill = convoLog.kids[convoLog.kids.length - 1];
check('a reader further up is not moved', convoLog.scrollTop === 200);
check('a pill at the foot says something arrived', pill && pill.attrs['class'] === 'ui-agent-new-below' && /New message/.test(pill.textContent));
followArrival(card(2400));
check('one pill however many arrive', convoLog.kids.filter(function(k) { return k === pill; }).length === 1);
check('and it keeps the first of them', pill.target === later);
// From the first arrival to the end is 2900 - (200 + 1800) = 900: more than
// the pane, so its top goes to the top.
pill.on.click();
check('pressing it takes them to the first, and goes away', pill.parentNode === null && convoStickToBottom && convoLog.scrollTop === 200 + 1800 - 8);
check('reaching the bottom puts it away', /if \(convoStickToBottom\) \{ convoReaderLeft = false; hideNewBelow\(\); \}/.test(panel));
check('it never carries a turn\'s tool calls', /c\.contains\('ui-agent-new-below'\)/.test(lift(panel, 'function notToolHost(node)', 'notToolHost')));

if (fail) process.exit(1);
