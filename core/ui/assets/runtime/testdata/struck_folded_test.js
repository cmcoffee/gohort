// A message taken back starts folded behind its reason line: the line stays in
// view with a control to show the struck text, so what was said is there to
// inspect without its whole length on screen. Folded only when there is a
// reason to stand in for it. Checked against the shipped source and styles.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var fn = src.slice(src.indexOf('function strikeMessage(id, reason) {'), src.indexOf('function labelMessage(id, label) {'));
check('no reason, nothing folded', /if \(!reason\) \{[\s\S]{0,120}classList\.remove\('ui-agent-msg-struck-folded'\);/.test(fn));
check('folded until opened', fn.indexOf("m.bubble.classList.toggle('ui-agent-msg-struck-folded', !m.struckOpen);") > 0);
check('the reason line carries the control', fn.indexOf("line.appendChild(toggle);") > 0);
check('the control says what it will do', fn.indexOf("toggle.textContent = m.struckOpen ? 'hide' : 'show';") > 0);
check('the open state survives a repaint (live, then replay)', fn.indexOf('m.struckOpen = !m.struckOpen; paint();') > 0);
check('the folded body is hidden', css.indexOf('.ui-agent-msg-struck-folded .ui-agent-msg-body { display: none; }') >= 0);

process.exit(fail ? 1 : 0);
