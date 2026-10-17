// uiOpenModal's onClose: told once, however the dialog went, so a caller that
// has to act on what happened inside it (the dashboard's section gear saves
// "seen" and redraws) is not left guessing whether Escape or a click outside
// closed it. Checked in the source of the close path every route runs.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}
var from = src.indexOf('window.uiOpenModal = function(opts) {');
var close = src.indexOf('function close() {', from);
var end = src.indexOf('function focusables()', close);
var body = src.slice(close, end);
check('the close path calls opts.onClose', /typeof opts\.onClose === 'function'/.test(body) && /opts\.onClose\(\)/.test(body));
check('only once: inside the released guard', /if \(!released\) \{[\s\S]*opts\.onClose\(\)[\s\S]*\}/.test(body));
check('a failing onClose cannot keep the dialog up', /try \{ opts\.onClose\(\); \} catch/.test(body));
check('Escape and the buttons reach the same close', /ev\.stopPropagation\(\);\s*close\(\);/.test(src.slice(close)));
console.log(fail === 0 ? '\nALL ONCLOSE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
