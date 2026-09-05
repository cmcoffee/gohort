// The composer belongs to the session on screen, and the rail says which
// sessions are working.
//
// Two things were wrong at once: the Send/Cancel state was panel-wide, so
// leaving a running session for an idle one left Cancel showing (bound to a
// session with nothing to cancel); and the rail's running pulse, styled in the
// stylesheet, was never emitted by the row builder. Checked against the shipped
// source, the way the other harnesses here do.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// Opening a session detaches the previous view's stream and, right after,
// resets the composer to idle — before the resume probe decides otherwise.
var open = src.indexOf('function openSession(sid, keepLimit) {');
var body = open > 0 ? src.slice(open, open + 6000) : '';
var detachAt = body.indexOf('detachActiveStream();');
var idleAt = body.indexOf('enableInput();', detachAt);
check('opening a session starts the composer idle, right after detaching the old stream',
  detachAt > 0 && idleAt > detachAt && idleAt - detachAt < 900);

// The only thing that may flip it back to Cancel is this session's own run.
var resume = src.indexOf('function tryResumeRun(sid) {');
check('the resume probe is what shows Cancel, and only on a run for THIS session',
  resume > 0 && /if \(!d \|\| !d\.run_id\) return;[\s\S]{0,400}disableInput\(\);/.test(src.slice(resume, resume + 900)));

// Cancel posts the OPEN session's id, so the button and the run agree.
check('cancel is keyed to the open session',
  /cfg\.cancel_url \+ '\?id=' \+ encodeURIComponent\(activeSessionId\)/.test(src));

// The rail row: a running session gets the pulse and the row class.
check('a running row emits the pulse the stylesheet has been carrying',
  /if \(rec\.running\) \{[\s\S]{0,200}ui-chat-side-running-dot/.test(src));
check('the running row is classed so the whole row reads as live',
  /if \(rec\.running\) \{\s*\n\s*rowClass \+= ' running';/.test(src));
check('the stylesheet styles the running row, not only the dot',
  css.indexOf('.ui-chat-side-item.running') > 0 && css.indexOf('.ui-chat-side-running-dot') > 0);

// While anything runs the rail re-reads itself, so the pulse goes out when the
// turn ends without a click — including a turn started in another tab.
check('the rail schedules its own refresh while a row is running',
  /function scheduleRunningRefresh\(items\)/.test(src) &&
  /scheduleRunningRefresh\(items\);/.test(src) &&
  /return s && s\.running;/.test(src));
check('one refresh timer at a time, and the render replaces a pending one',
  /if \(runningPollTimer\) \{ clearTimeout\(runningPollTimer\); runningPollTimer = null; \}/.test(src));

process.exit(fail ? 1 : 0);
