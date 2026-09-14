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

// The rail is MOUNTED IN THE DIALOG, so every control in its header is on
// screen there. Two of them steer a rail column that a modal panel does not
// have, and neither is hidden by the stylesheet at phone width — the collapse
// hamburger has no mobile rule at all, and the × is mobile-ONLY.
check('no collapse hamburger — there is no rail column to collapse',
  /var collapseBtn = listPosModal \? null :/.test(src));

check('the header survives having no left extras',
  /var leftExtras = collapseBtn \? \[collapseBtn\] : \[\];/.test(src));

check('the mobile × closes the picker, not a drawer that was never mounted',
  /if \(listPosModal\) closeSessionPicker\(\); else closeDrawer\(\);/.test(src));

// The header is built long before the picker; deciding this at the top is what
// lets all three places agree.
check('the mode is resolved before the rail header is built',
  src.indexOf("var listPosModal = hasList && cfg.list_position === 'modal';") <
  src.indexOf("side = el('div', {class: 'ui-chat-side'});"));

// A layout-forced rail state is not a user preference, and agent.sideCollapsed
// is one key shared by every agent-loop panel on the origin — so a modal panel
// forcing collapsed was teaching all of them to start collapsed.
check('a forced rail state is not written to the shared preference key',
  /if \(sideForced\) return;\s*\n\s*try \{ localStorage\.setItem\('agent\.sideCollapsed'/.test(src));

check('both forcing layouts mark the state as forced',
  (src.match(/sideForced = true;/g) || []).length >= 2);

// Starting a fresh conversation is not "a past session", and going through the
// list of them to reach it means opening a dialog to browse, not browsing, and
// closing it again — two clicks and a detour for the commoner of the two things
// this control is used for.
check('New sits beside the picker button, not inside it',
  /class: 'ui-row-btn', title: 'Start a new session',[\s\S]{0,120}?cfg\.new_label \|\| 'New'/.test(src));

// Clicking New while the picker happens to be open should leave the reader
// looking at the new session, not at a dialog over it.
check('New closes the picker if it is open',
  /onclick: function\(\)\{ closeSessionPicker\(\); openSession\(null\); \}/.test(src));

// A panel with a rail already has New in the rail header; two controls doing
// one job is how a toolbar stops being readable.
check('the pair is modal-mode only',
  /if \(listPosModal\) \{\s*\n\s*actionsBar\.appendChild/.test(src));

process.exit(fail ? 1 : 0);
