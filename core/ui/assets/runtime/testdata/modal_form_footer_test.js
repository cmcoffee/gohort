// A dialog that is a form puts the form's submit in its footer, beside Close.
//
// At the end of a long form the submit scrolled out of sight, and the dialog
// read as having nothing to press but Close (an Add Store dialog, reported).
// A ModalButton's dialog and a row action's open the same way
// (openComponentDialog); modal_footer_test.go drives the row's. A wizard's
// Back and Next go there too.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

check('a dialog whose body is a submit form gives it the footer',
  src.indexOf("var footerSubmit = c.type === 'form_panel' && !!c.submit_label;") >= 0 &&
  src.indexOf("if (footerSubmit) childCtx.__modalFooter = api.dialog.querySelector('.ui-modal-footer');") >= 0);
check('Close stops being the primary button there',
  src.indexOf("actions: footerSubmit ? [{label: 'Close'}] : undefined,") >= 0);
check('the form puts its submit in that footer, its outcome note by the fields',
  src.indexOf('var footer = !stepsMode && ctx && ctx.__modalFooter;') >= 0 &&
  /footer\.appendChild\(submitBtn\);[\s\S]{0,120}host\.appendChild\(msgEl\);/.test(src));

// A wizard in a dialog: Back, Close, then Next or the submit, in one footer
// row, the main button last; a redraw takes back what the last draw put there.
var stepper = src.slice(src.indexOf('function renderStepper(host) {'), src.indexOf('if (!stepsMode) {\n        appendTestRow(wrap);'));
check('a wizard puts Back before Close and Next and its submit after it',
  /foot\.insertBefore\(backBtn, foot\.firstChild\);\s*foot\.appendChild\(nextBtn\);/.test(stepper) &&
  /footSubmit\.className = 'ui-row-btn primary';\s*foot\.appendChild\(footSubmit\);/.test(stepper));
check('a redraw does not leave two of each',
  /if \(n\.dataset && n\.dataset\.wizardFoot\) n\.remove\(\);/.test(stepper));
check('the submit shows on the last step only, and a hidden Back leaves no gap',
  stepper.indexOf("if (footSubmit) footSubmit.style.display = isLast ? '' : 'none';") >= 0 &&
  stepper.indexOf("if (foot) backBtn.style.display = isFirst ? 'none' : '';") >= 0);

process.exit(fail ? 1 : 0);
