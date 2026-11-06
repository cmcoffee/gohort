// A row's status is a property of the ROW: "_status" is drawn as a short pill
// beside the card title, toned by "_status_tone" (ok, warn, bad, or none), and
// the layout does not learn what the status means.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('a row can carry a status pill', /if \(row\._status\) \{/.test(src));
check('the pill sits right after the title', /if \(ki === 0\) \{\s*info\.appendChild\(el\('span', \{style: 'font-weight:600[^\n]*\n(?:[^\n]*\n){0,5}\s*if \(row\._status\)/.test(src));
check('three tones from the theme', /var tones = \{ok: 'var\(--success[^}]*warn: 'var\(--warning[^}]*bad: 'var\(--danger/.test(src));
check('an unknown tone is neutral grey, not coloured', /\(tc \? 'background:color-mix\(in srgb, ' \+ tc \+ ' 18%, transparent\);color:' \+ tc : 'background:var\(--bg-2/.test(src));

process.exit(fail ? 1 : 0);
