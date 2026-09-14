// ui_notes_consumed — the framework's "the agent picked up your notes" signal.
//
// Marking a drained note is generic: the panel owns the composer, the bubble,
// its data-note-id and the inject contract. Two apps had each written their own
// near-identical renderer under their own block type, each in that app's page
// head — so an app that EMBEDS another app's chat received the server event
// with nothing registered to hear it.
var fs = require('fs');
var cards = fs.readFileSync(__dirname + '/../35_ask_cards.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('the framework registers it',
  /window\.uiRegisterBlockRenderer\('ui_notes_consumed'/.test(cards));

// Same guard as the ask cards beside it: an app wanting a richer version still
// wins, because app registration runs after this IIFE.
check('an app can still override it',
  /if \(!window\.UIBlockRenderers\.ui_notes_consumed\) \{/.test(cards));

check('it marks the bubble the id belongs to',
  /bubble\.classList\.add\('consumed'\)/.test(cards));

// A note id is server-issued and need not be a bare identifier; unescaped it
// would either match nothing or throw.
check('the id is escaped before it goes into a selector',
  /CSS\.escape \? CSS\.escape\(noteID\) : noteID/.test(cards));

// A note cannot be both read and unread. On a re-attach the panel may have
// given up on a note the server had in fact drained.
check('being read clears an earlier undelivered mark',
  /bubble\.classList\.remove\('ui-agent-interjection-undelivered'\)/.test(cards) &&
  /note\.remove\(\)/.test(cards));

// Nothing drained is not an activity line saying "0 notes".
check('an empty id list renders nothing',
  /if \(!n\) return null;/.test(cards));

check('the readout goes to the activity pane, not the conversation',
  /pane: 'activity'/.test(cards));

process.exit(fail ? 1 : 0);
