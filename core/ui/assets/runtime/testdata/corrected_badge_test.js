// A message a check followed up on carries a "Corrected" chip with the reason
// behind an info icon, live and on reopening, and is not struck: it said
// nothing untrue. Checked against the shipped source and styles.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var fn = src.slice(src.indexOf('function correctMessage(id, reason) {'), src.indexOf('function labelMessage(id, label) {'));
check('the renderer exists', fn.length > 0 && src.indexOf('function correctMessage(id, reason) {') >= 0);
check('the reason sits behind an info icon', fn.indexOf('uiInfoIcon(reason') > 0);
check('the chip names what happened', fn.indexOf("'Corrected'") > 0);
check('not struck', fn.indexOf('struck') < 0);
check('no reason, no chip', /if \(!reason\) return;/.test(fn));
check('applied live', /case 'chunk_corrected':\s*correctMessage\(ev\.id, ev\.reason \|\| ''\);/.test(src));
check('applied on reopening', src.indexOf('if (meta.corrected) correctMessage(id, meta.corrected);') > 0);
// The replay paths share savedMessageMeta, so the field is listed once.
var metaFn = src.slice(src.indexOf('function savedMessageMeta(m) {'), src.indexOf('function setMessageMeta(id, meta) {'));
check('the saved meta carries it', metaFn.indexOf('corrected: m.corrected') > 0 && /\|\| m\.corrected \|\|/.test(metaFn));
var replays = (src.match(/setMessageMeta\(mid, [^)]*\)/g) || []);
check('every replay path passes it on (3)', replays.length === 3 && replays.every(function(c) { return c === 'setMessageMeta(mid, meta)'; }));
check('styled', css.indexOf('.ui-agent-msg-corrected {') >= 0);

process.exit(fail ? 1 : 0);
