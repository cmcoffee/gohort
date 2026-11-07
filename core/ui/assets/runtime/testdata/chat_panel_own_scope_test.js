// The chat panel calls nothing that lives only inside the agent-loop panel.
//
// Each component is its own closure. openSession here called setLanding and
// altPinnedSession, which exist only inside agent_loop_panel's, so opening any
// session threw a ReferenceError and the panel never showed it.
var fs = require('fs');
var chat = fs.readFileSync(__dirname + '/../20_chat_panel.js', 'utf8');
var loop = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// Helpers declared inside the agent-loop panel's closure (indented, so not
// the file's own top level) that the chat panel neither declares nor gets.
var inner = {};
loop.replace(/\n {4,}function ([A-Za-z_$][\w$]*)\s*\(/g, function(_, n) { inner[n] = true; return _; });
var own = {};
chat.replace(/function ([A-Za-z_$][\w$]*)\s*\(/g, function(_, n) { own[n] = true; return _; });
chat.replace(/var ([A-Za-z_$][\w$]*)\s*=/g, function(_, n) { own[n] = true; return _; });
var borrowed = Object.keys(inner).filter(function(n) {
  if (own[n] || window_global(n)) return false;
  return new RegExp('(^|[^\\w$.])' + n.replace(/\$/g, '\\$') + '\\s*\\(').test(chat);
});
function window_global(n) { return /^ui[A-Z]/.test(n); }
check('the chat panel borrows no agent-loop helper: ' + borrowed.join(', '), borrowed.length === 0);

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
