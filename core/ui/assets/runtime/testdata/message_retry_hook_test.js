// Retry hands a bubble an app drew from more than its text to that app.
//
// Retry re-sent a message's text through commitUserEdit and nothing else. A
// bubble that went out with more than its text (a submitted form, whose values
// ride beside the message) was retried as plain text: it drew as text, and the
// server lost what had ridden with it. Edit already had a registry for exactly
// this (uiRegisterMessageEditor); retry gets its sibling.
var fs = require('fs');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('apps can register a retry override',
  /window\.uiRegisterMessageRetry = function\(matchFn, retryFn\)/.test(panel));

var fn = panel.slice(panel.indexOf('function retryUserMessage(bubble)'));
fn = fn.slice(0, fn.indexOf('\n    }\n') + 6);
check('retry asks the registered overrides first',
  fn.indexOf('messageRetriers') >= 0 && fn.indexOf('messageRetriers') < fn.lastIndexOf('commitUserEdit(bubble, raw)'));
check('an override gets the same commit the default path uses',
  /commit: function\(newText\) \{ return commitUserEdit\(bubble, newText\); \}/.test(fn));
check('a matched override ends the default path',
  /if \(!hit\) continue;[\s\S]*?return;\s*\}\s*commitUserEdit\(bubble, raw\)/.test(fn));
check('an override that fails says so',
  /\.catch\(failed\)/.test(fn) && /catch \(e\) \{ failed\(e\); \}/.test(fn));

if (fail) { console.log(fail + ' failed'); process.exit(1); }
