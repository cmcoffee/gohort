// A reader who scrolls up while a long reply streams stays where they went:
// the next chunk does not put them back at the bottom, and following starts
// again only once they return to the very end.
//
// Drives the real scroll code, lifted from the runtime source, over a
// stand-in pane.
var fs = require('fs');
var dir = __dirname + '/..';

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');
var start = panel.indexOf('    var convoStickToBottom = true;');
var endMark = 'convoTouchY = y;\n    }, {passive: true});';
var end = panel.indexOf(endMark, start);
if (start < 0 || end < 0) { console.log('FAIL the scroll code is gone'); process.exit(1); }
var block = panel.slice(start, end + endMark.length);

var handlers = {};
var convoLog = {
  scrollHeight: 2000, clientHeight: 500, _top: 1500,
  get scrollTop() { return this._top; },
  set scrollTop(v) { this._top = Math.max(0, Math.min(v, this.scrollHeight - this.clientHeight)); },
  addEventListener: function(name, fn) { handlers[name] = fn; },
  querySelectorAll: function() { return []; },
};
var timers = [];
function setTimeout(fn) { timers.push(fn); return timers.length; }
function clearTimeout() {}
function flush() { var t = timers; timers = []; t.forEach(function(f) { f(); }); }
var requestAnimationFrame;
eval(block);

function chunk(px) { convoLog.scrollHeight += px; scrollConvo(); handlers.scroll(); }
function userScrollTo(top) { convoLog.scrollTop = top; handlers.scroll(); }

chunk(100);
flush();
check('a streaming reply is followed', convoLog.scrollTop === convoLog.scrollHeight - convoLog.clientHeight);

// Up by a notch, and a chunk lands before the debounce runs.
var bottom = convoLog.scrollTop;
userScrollTo(bottom - 120);
var read = convoLog.scrollTop;
chunk(100);
check('the next chunk leaves the reader where they went', convoLog.scrollTop === read);
flush();
chunk(100);
check('and so does every chunk after the debounce', convoLog.scrollTop === read);

// A small step up, inside the old 80px tolerance, still counts.
userScrollTo(convoLog.scrollHeight - convoLog.clientHeight);
flush();
chunk(50);
check('back at the very end, following starts again', convoLog.scrollTop === convoLog.scrollHeight - convoLog.clientHeight);
userScrollTo(convoLog.scrollTop - 30);
flush();
var small = convoLog.scrollTop;
chunk(100);
check('a 30px step up is not undone', convoLog.scrollTop === small);

// A wheel upward holds the chunk that arrives before its scroll event.
userScrollTo(convoLog.scrollHeight - convoLog.clientHeight);
flush();
chunk(10);
var before = convoLog.scrollTop;
handlers.wheel({deltaY: -40});
convoLog.scrollHeight += 100;
scrollConvo();
check('a chunk right after a wheel upward does not scroll', convoLog.scrollTop === before);

// A send still scrolls, whatever the reader did.
scrollConvo(true);
check('a forced scroll goes to the end', convoLog.scrollTop === convoLog.scrollHeight - convoLog.clientHeight);

if (fail) { console.log(fail + ' failed'); process.exit(1); }
