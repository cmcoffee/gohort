// The per-member permission on a chip picker.
//
// Two pickers over overlapping sets read as two decisions and leave somebody
// working out which names are in both. One list with a setting per person is
// one decision, and this pins the wire shape that makes it one: membership in
// its own field, the extra permission in its own, both sent on every save so a
// name cannot outlive the membership it came with.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

// The flag is seeded from its OWN field on the fetched record, or a reload
// shows everybody back at the default.
check('the flag seeds from the record',
  /\(\(record && record\[cfg\.flag_field\]\) \|\| \[\]\)\.forEach/.test(src));

// Sent on EVERY save, and filtered to the current members: dropping somebody
// has to take their flag with them.
check('the flag is saved alongside membership', src.indexOf('body[cfg.flag_field] = held;') >= 0);
check('only current members keep the flag',
  /selected\.forEach\(function\(v\)\{ if \(flagged\[v\]\) held\.push\(v\); \}\)/.test(src));

// The control says which state it is in. "Make contributor" tells a reader
// nothing about the person in front of them.
check('the chip shows the state, not the action',
  src.indexOf("on ? (cfg.flag_label || 'Flagged') : (cfg.flag_off_label || 'Reader')") >= 0);

// A failed save puts it back. A toggle that sticks while the server refused is
// a screen that disagrees with the record behind it.
var i = src.indexOf('mark.addEventListener');
var block = i >= 0 ? src.slice(i, i + 400) : '';
check('a refused toggle rolls back', /Save failed/.test(block) && (block.match(/flagged\[v\] = !flagged\[v\]/g) || []).length === 2,
  JSON.stringify(block.slice(0, 120)));

process.exit(fail ? 1 : 0);
