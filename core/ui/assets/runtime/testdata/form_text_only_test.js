// A submit form whose fields are all headings has nothing to send: it shows
// no submit. Suggested sources and Fill it for me read their fields from the
// server, and while there is nothing to tick or run they are one heading
// saying so, under a button that could only answer with an error.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
var fn = src.slice(src.indexOf('      function appendSubmit(host) {'));
fn = fn.slice(0, fn.indexOf("var submitBtn = el('button'"));
check('a form of headings only offers no submit, a wizard and an empty field list aside',
  fn.indexOf("if (!stepsMode && shownFields.length && shownFields.every(function(f) { return f && (f.type || 'text') === 'header'; })) return;") >= 0);
check('the rule is read before the button is made', fn.indexOf('if (!submitMode) return;') >= 0 && fn.indexOf('shownFields') > fn.indexOf('if (!submitMode) return;'));
console.log(fail === 0 ? '\nALL FORM TEXT ONLY TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
