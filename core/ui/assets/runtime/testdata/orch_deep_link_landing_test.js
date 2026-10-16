// A ?session link to a cortex agent keeps the session it opened. On load the
// panel's first pass can run before the host page has said which agent is
// picked (it sees none); the host's agent event then arrives. Going from none
// to the picked agent is the same page load, not a switch, so it must not
// land on a new conversation over the linked session. A real switch, from one
// agent to another, still lands fresh.
var fs = require('fs');
var dir = __dirname + '/..';
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(src, start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}
var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

var cfg = {orchestrator_nav: []};
var orchBtns = [], navMenus = [], pinnedEl = null, navTopbarEl = null, primaryEl = null, orchView = null;
var hasAllAgentNav = false;
var opened = [];
function isAltNavAgent(id) { return id === 'cortex-agent' || id === 'other-agent'; }
function recordPinnedSession() { return ''; }
function flaggedFor() { return false; }
function refreshChannelBadges() {}
function closeNavMenus() {}
function clearTopbarNavSelection() {}
function getLanding() { return ''; }
function altPinnedSession() { return ''; }
function openHomeThread() { opened.push('home'); }
function openSession(sid) { opened.push(sid); if (!sid) activeSessionId = ''; }
var activeSessionId = '';
var lastOrchAgent;
eval(lift(panel, 'function applyOrchMode(agentId)', 'applyOrchMode'));

// Deep link: the link set the session, then the two passes arrive.
activeSessionId = 'linked';
applyOrchMode('');             // the panel's own pass, before the agent is known
applyOrchMode('cortex-agent'); // the host page's agent event
check('a linked session survives the first load', activeSessionId === 'linked' && opened.length === 0);

// No link: the agent's landing still happens.
lastOrchAgent = undefined; opened = []; activeSessionId = '';
applyOrchMode('');
applyOrchMode('cortex-agent');
check('with no link the agent lands on a new conversation', opened.length === 1 && opened[0] === null);

// A real switch re-lands even with a session open.
lastOrchAgent = undefined; opened = []; activeSessionId = 'linked';
applyOrchMode('cortex-agent');
applyOrchMode('other-agent');
check('switching agents lands on a new conversation', opened.length === 1 && opened[0] === null);

if (fail) process.exit(1);
