// Editing an earlier message: on a desktop, in a box where the message is;
// on a phone, in the composer (the box there sat under the keyboard). The
// choice is lifted and run against both kinds of screen.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}
var from = src.indexOf('    function editsInComposer() {');
var to = src.indexOf('    // beginBubbleEdit opens the edit box');
if (from < 0 || to < from) { console.log('FAIL editsInComposer moved'); process.exit(1); }
function screen(width, coarse) {
  var win = {matchMedia: function(q) {
    if (q === '(max-width: 700px)') return {matches: width <= 700};
    if (q === '(pointer: coarse)') return {matches: coarse};
    return {matches: false};
  }};
  return new Function('window', src.slice(from, to) + '\nreturn editsInComposer();')(win);
}
check('a desktop edits in place', screen(1440, false) === false);
check('a narrow window edits in the composer', screen(600, false) === true);
check('a touch screen edits in the composer', screen(1024, true) === true);

var begin = src.slice(src.indexOf('    function beginUserEdit(bubble) {'), src.indexOf('    function editsInComposer() {'));
check('an app editor still comes first', begin.indexOf('messageEditors') < begin.indexOf('editsInComposer()'));
check('the choice picks between the two', /if \(editsInComposer\(\)\) beginComposerEdit\(bubble, raw\);\s*else beginBubbleEdit\(bubble, body, actions, raw\);/.test(begin));

var box = src.slice(src.indexOf('    function beginBubbleEdit('), src.indexOf('    // On a phone an earlier message is edited in the composer'));
check('the box offers Save & resend and Cancel', /Save & resend/.test(box) && /\['Cancel'\]/.test(box));
check('it saves through the same truncate-and-resend', /commitUserEdit\(bubble, newText\)/.test(box));
check('Enter saves, Shift+Enter is a new line', /ev\.key === 'Enter' && !ev\.shiftKey && !ev\.isComposing/.test(box));
check('Escape cancels', /ev\.key === 'Escape'[\s\S]{0,40}close\(\)/.test(box));
check('a second Edit focuses the open box rather than opening another', /ui-agent-msg-edit-bar'\);\s*if \(open\)/.test(box));

console.log(fail === 0 ? '\nALL MESSAGE EDIT TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
