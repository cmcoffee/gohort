// A queued note the agent never read has to say so.
//
// The runner drains the note queue BETWEEN ROUNDS. If the turn stops first —
// cancelled, finished, or failed — nothing read it, and until now nothing said
// so: the bubble kept the dim "queued" style, which is also exactly how it
// looks while it is still waiting. On reload the server had appended it to the
// session as an ordinary user message, indistinguishable from one that was
// answered.
var fs = require('fs');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css   = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('there is a marker for a note nobody read',
  /function markUndeliveredInterjections\(why\)/.test(panel));

// All three ways a turn can end without draining.
check('cancel marks it',
  /markUndeliveredInterjections\('The agent was stopped before reading this/.test(panel));
check('a finished turn marks it',
  /markUndeliveredInterjections\('The agent finished before reading this/.test(panel));
check('a failed turn marks it',
  /markUndeliveredInterjections\('The turn failed before the agent read this/.test(panel));

// enableInput also fires on session OPEN. Hooking it would condemn a note
// belonging to a run that is still going.
check('the marker is not hooked into enableInput',
  !/function enableInput\(\)[\s\S]{0,400}?markUndeliveredInterjections/.test(panel));

// Cancel must say it before the POST — the agent has stopped reading by then.
var cancelBlock = panel.slice(panel.indexOf('cancelLabel.textContent = \'Cancelling…\''));
cancelBlock = cancelBlock.slice(0, cancelBlock.indexOf('function detachActiveStream'));
check('cancel marks before it posts',
  cancelBlock.indexOf('markUndeliveredInterjections') < cancelBlock.indexOf('cfg.cancel_url'));

// It is not deleted: the server keeps a leftover note by appending it to the
// session, so the text really does reach the agent next message. Dropping the
// bubble would claim the opposite.
check('the note is marked, not removed',
  !/markUndeliveredInterjections[\s\S]{0,600}?\.remove\(\)/.test(panel));

// Settled is settled — a cancelled note dragged to the bottom of every later
// turn would go on claiming it is pending.
check('a settled note stops being re-anchored',
  /'\.ui-agent-interjection:not\(\.consumed\):not\(\.ui-agent-interjection-undelivered\)'/.test(panel));

// Marked once. A second cancel must not stack a second explanation onto it.
check('marking is idempotent',
  /:not\(\.ui-agent-interjection-undelivered\)'\);\s*\n\s*for \(var i = 0/.test(panel));

check('the state is visually distinct from still-waiting and from failed-to-send',
  /\.ui-agent-interjection-undelivered \.ui-agent-msg-body/.test(css) &&
  /\.ui-agent-interjection-note \{/.test(css));

process.exit(fail ? 1 : 0);
