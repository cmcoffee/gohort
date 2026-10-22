// A conversation belongs to the record it is about. A panel whose URLs name the
// host's open record ({scope}) starts a new session when that record changes to
// another, and lists the new record's sessions; kept, the open conversation
// carried its history into the next document. Checked against the shipped
// source.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var at = src.indexOf('var scopedPanel = ');
var block = at > 0 ? src.slice(at, at + 900) : '';
check('only a panel scoped to a record watches it',
  /var scopedPanel = \[cfg\.send_url, cfg\.list_url\]\.some\(function\(u\) \{ return \(u \|\| ''\)\.indexOf\('\{scope\}'\) >= 0; \}\);/.test(src));
check('it watches the scope attribute', block.indexOf("attributeFilter: ['data-ui-scope']") > 0);
check('a first open is not a switch', /if \(!was \|\| !now\) return;/.test(block));
check('a switch opens a new session', block.indexOf('openSession(null);') > 0);
check('and lists the new record\'s sessions', block.indexOf('loadSessions();') > block.indexOf('openSession(null);'));

process.exit(fail ? 1 : 0);
