// RowAction "number" with Decimals takes a fraction (a price), and with
// PlaceholderField shows the row's own fallback greyed in the empty box and
// posts null when the box is emptied, so the row goes back to it rather than
// to 0. Without either it is the whole-number box it was.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var i = src.indexOf("} else if (act.type === 'number') {");
var j = src.indexOf("} else if (act.type === 'button') {", i);
var body = i >= 0 && j > i ? src.slice(i, j) : '';
check('the number branch is found', body.length > 0);
check('decimals step the box and parse a fraction',
  body.indexOf("if (act.decimals) ninput.step = String(Math.pow(10, -act.decimals));") >= 0 &&
  body.indexOf("var n = act.decimals ? parseFloat(ninput.value) : parseInt(ninput.value, 10);") >= 0);
check("the row's own fallback is the box's placeholder",
  /if \(act\.placeholder_field && rec\[act\.placeholder_field\] != null[^\n]*\) \{\s*\n\s*ninput\.placeholder = String\(rec\[act\.placeholder_field\]\);/.test(body));
check('an emptied box posts null with a placeholder field, else 0',
  body.indexOf("if (isNaN(n)) n = act.placeholder_field ? null : 0;") >= 0);
check('a set 0 still shows when the row has a fallback',
  body.indexOf("(v == null || (v === 0 && !act.placeholder_field)) ? ''") >= 0);

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
