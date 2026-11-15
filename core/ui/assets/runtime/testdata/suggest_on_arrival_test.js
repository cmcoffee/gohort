// A field that drafts itself as its step opens (suggest_on_open) fills its own
// box. A long one (textarea, rules) used to open the assist workbench on
// arrival, covering the step just reached; now only its ✨ button opens the
// workbench, and the arrival takes the one-shot draft.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
var fn = src.slice(src.indexOf('async function runFieldSuggest(f, btn, auto) {'));
fn = fn.slice(0, fn.indexOf('postSuggest(f,'));
check('the workbench opens for a pressed button, or a cold suggest, not for an arrival',
  fn.indexOf("if (assistFieldTypes[f.type || 'text'] && (btn || !auto)) {") >= 0);
check('an arrival is a call with no button',
  src.indexOf('if (cur == null || String(cur).trim() === \'\') runFieldSuggest(f, null, true);') >= 0);
check('a long field\'s button is not labelled as a re-roll',
  src.indexOf("[autoSuggest && !assistFieldTypes[t] ? '✨ Suggest again' : '✨ Suggest']") >= 0);
console.log(fail === 0 ? '\nALL SUGGEST ON ARRIVAL TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
