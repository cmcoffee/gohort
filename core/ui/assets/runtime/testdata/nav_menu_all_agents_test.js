// all_agents on a MENU entry — the nav dropdowns follow their contents.
//
// The flag says an item's data is user-scoped rather than agent-scoped, so the
// item renders for every agent. It used to be honored only for pinned rows and
// topbar controls: the menu control was shown or hidden wholesale on the
// alt-nav opt-in, before the per-item loop ran, so an exemption declared inside
// a menu could never be reached. These check the shipped source, which is what
// a host app's Go is relying on.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// One predicate, consulted everywhere an item can render — the bug was two
// rules disagreeing about what all_agents means depending on placement.
check('visibility is one shared predicate',
  /var navOn = function\(i\) \{[\s\S]{0,200}?return isOrch \|\| !!item\.all_agents \|\| \(!!item\.record_too && isRecord\);/.test(src));

// record_too: an action on the pinned thread (clearing it) applies to an
// agent whose pinned thread is a record, which has no alt nav of its own.
check('record_too shows an item for a record agent',
  /var isRecord = !!recordPinnedSession\(agentId\);/.test(src));
check('an action on a record agent refreshes its list rather than a home thread',
  /var rec = recordPinnedSession\(window\.GOHORT_AGENT_ID\);[\s\S]{0,200}?loadSessions\(\);/.test(src));

check('a menu is shown when it still holds something',
  /m\.control\.style\.display = m\.items\.some\(navOn\) \? '' : 'none';/.test(src));

// The old line: unconditional on isOrch, and ahead of the per-item loop.
check('the menu is no longer gated wholesale on the alt nav',
  !/navMenus\.forEach\(function\(m\) \{ m\.control\.style\.display = isOrch \? '' : 'none'; \}\);/.test(src));

// A heading names the rows under it; with all of them gone it names nothing.
check('group headings are tracked so they can go with their rows',
  /menu\.hdrs\.push\(\{group: item\.group, el: ghdr\}\);/.test(src) &&
  /\(m\.hdrs \|\| \[\]\)\.forEach\(function\(h\) \{/.test(src));

// Menu items were skipped by the badge refresh because it tested placement.
check('badge refresh keys off the flag, not the placement',
  /if \(onlyAllAgents && !item\.all_agents\) return;/.test(src));

process.exit(fail ? 1 : 0);
