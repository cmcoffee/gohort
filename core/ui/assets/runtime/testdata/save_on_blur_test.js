// FormField.SaveOnBlur: a text field whose every save acts at once (an LLM
// form rebuilds the live model on each one) saves when it is left or on
// Enter, never on the typing pause, which saved half-typed model ids and
// tried each one.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('typing does not save a save_on_blur field',
  /if \(!f\.reload_on_change && !f\.save_on_blur\) \{\s*\n\s*input\.addEventListener\('input', function\(\)\{ debounced\(f\.field, input\.value\); \}\);/.test(src));
check('leaving the field saves it',
  /input\.addEventListener\('blur', function\(\)\{\s*\n\s*clearTimeout\(debounceTimers\[f\.field\]\);\s*\n\s*if \(current\[f\.field\] !== input\.value\) save\(f\.field, input\.value\);/.test(src));
check('Enter saves it too',
  /if \(f\.save_on_blur\) \{\s*\n\s*input\.addEventListener\('keydown', function\(ev\)\{\s*\n\s*if \(ev\.key !== 'Enter'\) return;[\s\S]{0,120}save\(f\.field, input\.value\);/.test(src));

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
