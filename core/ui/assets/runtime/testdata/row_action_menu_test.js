// RowAction.Group: a row's grouped actions sit under one "<group> ▾" button,
// not beside the others, and the menu is built when the button is pressed
// and removed when it closes, so rows rebuilt on every refresh leave none
// behind on the page.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check("a grouped action is held back from the row's buttons",
  /if \(act\.group\) \{[\s\S]{0,200}actionGroups\[act\.group\]\.push\(\{act: act, ai: ai\}\);\s*\n\s*return;/.test(src));
check("each group becomes a menu after the row's buttons",
  /actionGroupOrder\.forEach\(function\(g\) \{\s*\n\s*appendActionMenu\(actionsWrap, g, actionGroups\[g\], rec, rowKey, row\);/.test(src));
var i = src.indexOf('function appendActionMenu(');
var body = i >= 0 ? src.slice(i, i + 2600) : '';
check('the menu respects only_if and hide_if, and an empty one is not shown',
  body.indexOf('it.act.only_if') >= 0 && body.indexOf('it.act.hide_if') >= 0 && body.indexOf('if (!shown.length) return;') >= 0);
check('the menu is built when its button is pressed', /toggle\.addEventListener\('click', function\(ev\) \{[\s\S]{0,200}var menu = el\('div', \{class: 'ui-row-menu'/.test(body));
check('closing removes it from the page and its outside listener',
  /open\.menu\.parentNode\.removeChild\(open\.menu\)/.test(body) && /document\.removeEventListener\('click', open\.outside, true\)/.test(body));

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
