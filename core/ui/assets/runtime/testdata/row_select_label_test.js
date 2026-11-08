// RowAction "select" with a Label: the dropdown sits beside its caption, as a
// toggle and a segmented track do, so a row's dropdown says what it answers;
// with no Label it stands alone, as before.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var i = src.indexOf("} else if (act.type === 'select') {");
var j = src.indexOf("} else if (act.type === 'segmented') {", i);
var body = i >= 0 && j > i ? src.slice(i, j) : '';
check('the select branch is found', body.length > 0);
check('a labelled select is captioned with the toggle pairing',
  /if \(act\.label\) \{\s*\n\s*parent\.appendChild\(el\('span', \{class: 'ui-row-toggle-pair'\}, \[\s*\n\s*el\('span', \{class: 'ui-row-toggle-label'\}, \[act\.label\]\),\s*\n\s*sel,/.test(body));
check('an unlabelled select stands alone', /\} else \{\s*\n\s*parent\.appendChild\(sel\);\s*\n\s*\}/.test(body));

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
