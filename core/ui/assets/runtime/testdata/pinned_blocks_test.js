// A block that follows the running turn stays at the top of the thread: the
// newest live one while a turn runs, a done one for a moment after, nothing
// once the turn has ended, and the marked step kept in view inside it.
//
// Drives the real functions, lifted from the runtime source, over a
// stand-in log and clock.
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
var css = fs.readFileSync(dir + '/../runtime.css', 'utf8');

var timers = [];
var setTimeout = function(fn) { timers.push(fn); return timers.length; };
var clearTimeout = function(id) { if (id) timers[id - 1] = null; };
function fireTimers() { var t = timers; timers = []; t.forEach(function(f) { if (f) f(); }); }

function block(pin) {
  var attrs = {};
  if (pin) attrs['data-ui-pin'] = pin;
  var cls = {};
  return {
    attrs: attrs, parentNode: null, scrollTop: 0, clientHeight: 100, focus: null,
    getAttribute: function(k) { return k in attrs ? attrs[k] : null; },
    setAttribute: function(k, v) { attrs[k] = v; },
    classList: {add: function(c) { cls[c] = 1; }, remove: function(c) { delete cls[c]; }, has: function(c) { return !!cls[c]; }},
    querySelector: function() { return this.focus; },
  };
}
var convoLog = {
  kids: [],
  appendChild: function(n) { this.kids.push(n); n.parentNode = this; },
  querySelectorAll: function(sel) {
    return this.kids.filter(function(n) { return n.getAttribute('data-ui-pin') === 'live'; });
  },
};
function pinned(b) { return b.classList.has('ui-agent-pinned'); }

eval('var turnLive = false; var pinnedWrap = null, pinDoneTimer = null, pinDoneMs = 4000;');
eval(lift(panel, 'function unpin()', 'unpin'));
eval(lift(panel, 'function refreshPins()', 'refreshPins'));
eval(lift(panel, 'function followPinFocus(w)', 'followPinFocus'));

var plan = block('live');
convoLog.appendChild(plan);
refreshPins();
check('nothing is pinned while no turn runs (a plan left unfinished stays in its place)', !pinned(plan));

turnLive = true;
refreshPins();
check('a live block is pinned while the turn runs', pinned(plan));

var plan2 = block('live');
convoLog.appendChild(plan2);
refreshPins();
check('the newest live block takes the pin', pinned(plan2) && !pinned(plan));

plan2.focus = {offsetTop: 400, offsetHeight: 20};
refreshPins();
check('the marked step is scrolled into view inside the block', plan2.scrollTop > 300 && plan2.scrollTop <= 400);

plan2.setAttribute('data-ui-pin', 'done');
plan.setAttribute('data-ui-pin', 'done');
refreshPins();
check('a block that turns done stays pinned for a moment', pinned(plan2) && timers.length === 1);
fireTimers();
check('then goes back to its place in the thread', !pinned(plan2));

var plan3 = block('live');
convoLog.appendChild(plan3);
refreshPins();
turnLive = false;
refreshPins();
check('the turn ending unpins a block still live', !pinned(plan3));

check('the pinned style sticks to the top, capped, scrolling inside',
  /\.ui-agent-convo-log > \.ui-agent-pinned \{[^}]*position: sticky; top: 0;[^}]*max-height: 33vh; overflow-y: auto;/.test(css));
check('disableInput and enableInput set the turn and re-read the pins',
  /turnLive = true;\s*refreshPins\(\);/.test(panel) && /turnLive = false;\s*refreshPins\(\);/.test(panel));
check('a block update re-reads the pins',
  /existing\.onUpdate\(d\); \} catch \(_\) \{\}\s*refreshPins\(\);/.test(panel));

if (fail) process.exit(1);
