// An option with an action is a command, not a choice: it is drawn with a
// value no real option has, picking it runs its client action with the
// option's value and puts the select back, and it is never saved or seeded
// as the field's value.
var fs = require('fs');
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var keySrc = basics.match(/    function selectActionKey\(o\) \{[^\n]*\}\n/);
var runSrc = basics.match(/    function selectAction\(o, f\) \{[\s\S]*?\n    \}\n/);
check('the helpers are in the form code', !!keySrc && !!runSrc);
var toasts = [];
function showToast(m) { toasts.push(m); }
var window = {UIClientActions: {}};
eval(keySrc[0]);
eval(runSrc[0]);

var add = {value: 'web.search', label: '+ Add another…', action: 'connector_add_another'};
var key = selectActionKey(add);
check('its value is no real value', key !== 'web.search' && key.indexOf('\u0001') === 0);
var got = null;
window.UIClientActions.connector_add_another = function(ctx) { got = ctx; };
selectAction(add, {field: 'provider'});
check('picking it runs its action with its value and field', got && got.value === 'web.search' && got.field === 'provider');
delete window.UIClientActions.connector_add_another;
selectAction(add, {field: 'provider'});
check('an action nothing handles says so', toasts.length === 1 && /connector_add_another/.test(toasts[0]));

var sel = basics.match(/\} else if \(t === 'select'\) \{([\s\S]*?)\n      \} else if/);
check('the select draws an action option by its key', !!sel && /if \(o\.action && !multi\) \{\s*var key = selectActionKey\(o\);\s*actionOpts\[key\] = o;/.test(sel[1]));
check('picking one puts the select back and runs it, saving nothing',
  !!sel && /if \(!multi && actionOpts\[input\.value\]\) \{\s*var o = actionOpts\[input\.value\];\s*input\.value = heldValue;\s*selectAction\(o, f\);\s*return;/.test(sel[1]));
check('an action option is never seeded as the value', !!sel && /&& !f\.options\[0\]\.action\)/.test(sel[1]));
check('a choice is remembered as what to go back to', !!sel && /if \(!multi\) \{ heldValue = input\.value; save\(f\.field, input\.value\); return; \}/.test(sel[1]));

if (fail) { console.log(fail + ' FAIL'); process.exit(1); }
