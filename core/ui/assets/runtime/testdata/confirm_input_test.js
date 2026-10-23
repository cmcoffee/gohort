// An answer on a confirm card can carry text the person checks before it is
// sent (action.input): choosing it swaps the buttons for the field, prefilled,
// with its label and help, a send button named for the answer, and Back. The
// text goes out as `input` beside the value; answers without one post as they
// did. A refusal re-enables the field rather than settling the card. Checked
// against the shipped source and styles.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var add = src.slice(src.indexOf('function addConfirm(d) {'), src.indexOf('function settleConfirmCard('));
check('an answer with input opens the field instead of sending',
  add.indexOf('if (a.input) { editConfirmInput(id, a, card, btns); return; }') > 0);

var edit = src.slice(src.indexOf('function editConfirmInput('), src.indexOf('function submitConfirm('));
check('the field starts with the suggested text', edit.indexOf("value: spec.value || ''") > 0);
check('the label and help are shown', edit.indexOf('spec.label') > 0 && edit.indexOf('spec.help') > 0);
check('sending carries what is in the field', edit.indexOf('submitConfirm(id, action, card, send, field.value)') > 0);
check('Back puts the buttons back', edit.indexOf('row.replaceWith(btns)') > 0);
check('the field row is replaced when the card settles', edit.indexOf("class: 'ui-agent-confirm-btns ui-agent-confirm-edit'") > 0);

var submit = src.slice(src.indexOf('function submitConfirm('), src.indexOf('window.uiResolveBlock'));
check('input rides with the value', submit.indexOf('{id: id, value: value, input: input}') > 0);
check('an answer without input posts as before', submit.indexOf('input === undefined ? {id: id, value: value}') > 0);
check('a refusal re-enables to fix it', /catch\(function\(err\) \{[\s\S]{0,200}b\.disabled = false/.test(submit));

check('the field is styled', css.indexOf('.ui-agent-confirm-input {') >= 0 && css.indexOf('.ui-agent-confirm-edit {') >= 0);

process.exit(fail ? 1 : 0);
