// A row that needs attention is a property of the ROW, like its depth: the
// server says "_warn" and why in "_warn_why", and the cards layout draws a
// warning edge, a dot ahead of the title, and the reason as visible text,
// without learning what the attention is about.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('a row can declare it needs attention', /var warn = !!row\._warn;/.test(src));
check('it gets a warning edge in the theme colour', /if \(warn\) card\.style\.borderLeft = '3px solid var\(--warning/.test(src));
check('a dot leads the title, named for a screen reader', /class: 'ui-card-warn', role: 'img', 'aria-label': 'Needs attention'/.test(src));
// Visible, not only a tooltip: a phone has no hover.
check('the reason is visible text', /if \(warn && row\._warn_why\) \{\s*info\.appendChild\(cardText\(String\(row\._warn_why\), '[^']*', 'ui-card-warn-why'\)\)/.test(src));
// A row that declares nothing renders as before.
check('no _warn, no marker', /if \(warn\) \{\s*info\.appendChild/.test(src));

process.exit(fail ? 1 : 0);
