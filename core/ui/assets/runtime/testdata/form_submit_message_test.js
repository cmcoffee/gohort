// A form in a dialog closes on submit, so the server's own message (what to
// do next, what the save could not do) has to be the toast: the form's message
// line closes with it.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
check('a submitted form toasts the server message before the default',
  /showToast\(\(resp && typeof resp\.message === 'string' && resp\.message\) \|\| cfg\.saved_toast \|\| 'Saved ✓'\);/.test(src));
console.log(fail === 0 ? '\nALL FORM SUBMIT MESSAGE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
