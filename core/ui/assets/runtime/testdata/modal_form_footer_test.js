// A dialog that is a form puts the form's submit in its footer, beside Close.
//
// At the end of a long form the submit scrolled out of sight, and the dialog
// read as having nothing to press but Close (an Add Store dialog, reported).

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

check('a dialog whose body is a submit form gives it the footer',
  src.indexOf("var footerSubmit = b.type === 'form_panel' && !!b.submit_label && !(b.steps && b.steps.length);") >= 0 &&
  src.indexOf("if (footerSubmit) childCtx.__modalFooter = api.dialog.querySelector('.ui-modal-footer');") >= 0);
check('Close stops being the primary button there',
  src.indexOf("actions: footerSubmit ? [{label: 'Close'}] : undefined,") >= 0);
check('the form puts its submit in that footer, its outcome note by the fields',
  src.indexOf('var footer = !stepsMode && ctx && ctx.__modalFooter;') >= 0 &&
  /footer\.appendChild\(submitBtn\);[\s\S]{0,120}host\.appendChild\(msgEl\);/.test(src));

process.exit(fail ? 1 : 0);
