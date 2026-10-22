// Counts and the session list follow what happens, not a reload.
//
// An agent adding a schedule left the Scheduler's count where it was until
// the page was reloaded, and a scheduled run writing to the cortex left its
// "new activity" dot off: the counts were read on navigation and on a timer
// that reads only the queues, and the list's timer skips an agent whose
// cortex is a record. The live pill already looks at running work every few
// seconds; it now says when some starts or ends, and the agent page refreshes
// on that, on a turn's tool results and at the end of a turn.
var fs = require('fs');
var epi = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// The live pill's change detection, run as shipped.
var a = epi.indexOf('      var liveSeen = null;');
var b = epi.indexOf('      function refreshLive() {');
if (a < 0 || b < a) { console.log('FAIL the live change detection is gone'); process.exit(1); }
var heard = [];
var window = {dispatchEvent: function(e) { heard.push(e.detail); }};
function CustomEvent(name, o) { this.name = name; this.detail = o.detail; }
eval(epi.slice(a, b));

noteLiveChange([{id: 'r1'}, {id: 'r2'}]);
check('the first look announces nothing: it is what was already running', heard.length === 0);
noteLiveChange([{id: 'r1'}, {id: 'r2'}]);
check('nothing changed, nothing announced', heard.length === 0);
noteLiveChange([{id: 'r1'}, {id: 'r3'}]);
check('work ending and starting is announced: ' + JSON.stringify(heard),
  heard.length === 1 && heard[0].ended.join() === 'r2' && heard[0].started.join() === 'r3');
noteLiveChange([]);
check('the last of it ending is announced too', heard.length === 2 && heard[1].ended.sort().join() === 'r1,r3');
check('spawned work counts as work: the change is read before the pill filters it',
  /noteLiveChange\(items \|\| \[\]\);\s*items = \(items \|\| \[\]\)\.filter\(function\(it\)\{ return !it\.spawned; \}\);/.test(epi));

// The agent page's side of it.
check('a live change refreshes the counts and the list',
  /window\.addEventListener\('ui-live-changed', function\(ev\) \{[\s\S]{0,160}?refreshAfterChange\(\);/.test(panel));
check('the refresh reads the counts for the agent on screen, and the list',
  /if \(isAltNavAgent\(window\.GOHORT_AGENT_ID\)\) refreshChannelBadges\(\);\s*else if \(hasAllAgentNav\) refreshChannelBadges\(true\);\s*if \(hasList\) loadSessions\(\);/.test(panel));
check('a tool result refreshes them, so a schedule added mid-turn shows then',
  /attachAgentToolToggle\(host2\); \/\/ refresh count \+ open panel\s*scrollConvo\(false\);\s*if \(afterChange\) afterChange\(\);/.test(panel));
check('the end of a turn refreshes them',
  /\/\/ And the nav's counts, which a tool in the turn may have moved\.\s*if \(afterChange\) afterChange\(\);/.test(panel));
check('the hook is set where the nav is built', /afterChange = refreshAfterChange;/.test(panel));

process.exit(fail ? 1 : 0);
