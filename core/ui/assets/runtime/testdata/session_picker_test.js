// list_position:"modal" — the sessions list as a button rather than a rail.
//
// The three things that make it worth having over a collapsed rail, checked
// against the shipped source: the rail is NOT put in the layout grid, the
// toolbar gets a control that opens it, and picking a session closes the
// dialog rather than leaving the reader to dismiss a list they are done with.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('the mode exists',
  /var listPosModal = hasList && cfg\.list_position === 'modal';/.test(src));

// The rail element is reused, not rebuilt — that is what keeps search, unread
// marks, rename and delete working inside the dialog for free.
check('the picker mounts the rail itself',
  /body\.appendChild\(side\);/.test(src));

check('the rail stays out of the layout grid in modal mode',
  /if \(hasList && !listPosModal\) \{\s*\n\s*gridRow\.appendChild\(side\);/.test(src));

check('no floating expand tab to go with a rail that is not there',
  /if \(expandTab && !listPosModal\)/.test(src));

check("no mobile drawer header either — it would open a rail that lives in a dialog",
  /if \(drawer && !listPosModal\) main\.appendChild\(drawer\.mobileHdr\);/.test(src));

check('the toolbar gets the control',
  /if \(listPosModal\) \{[\s\S]{0,220}onclick: function\(\)\{ openSessionPicker\(\); \}/.test(src));

// Cutting to the session is the whole point of preferring this to a rail.
var open = src.indexOf('function openSession(sid, keepLimit) {');
check('picking a session closes the picker',
  open > 0 && src.slice(open, open + 400).indexOf('closeSessionPicker();') > 0);

// A stored "expanded" preference from the rail mode would otherwise leave an
// empty 260px column beside the chat.
check('the grid is forced to one column',
  /if \(listPosModal\) \{\s*\n\s*sideCollapsed = true;/.test(src));

// Reopening must not leave a stale handle that makes the button inert.
check('the handle is cleared on close, so the button works twice',
  /sessionModal\.close = function\(\) \{ closed\(\); sessionModal = null; \};/.test(src));

process.exit(fail ? 1 : 0);
