// The bell is CHROME, and chrome has one hard rule: it cannot take the page
// down. Every page the framework renders gets this, including ones served
// where notifications are not reachable at all, so each failure mode has to
// have a defined quiet answer rather than an exception.
//
// It is also the first thing in the shared runtime to call a deployment-wide
// API, so this pins that it names no app: core/ui is domain-agnostic, and a
// bell that knew which app wrote a notice would be the leak that rule exists
// to stop.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../71_notice_bell.js', 'utf8');
var header = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('it is mounted on the shared page header',
  /header\.appendChild\(uiNoticeBell\(\)\);/.test(header));

check('it speaks only to the framework endpoint',
  /'\/api\/notifications'/.test(src) &&
  /\/api\/notifications\/read/.test(src) &&
  /\/api\/notifications\/dismiss/.test(src));

// The rule from CLAUDE.md, checked rather than trusted: nothing in core/ui may
// name an app.
//
// The names are READ from the tree rather than listed here. A hardcoded list
// goes stale the moment somebody adds an app, which is exactly when this check
// stops being worth having; and a test that has to spell out every app name in
// order to forbid them is itself a file in core/ui naming apps, which the
// repository's own pre-commit guard refuses, correctly.
var root = __dirname + '/../../../../..';
var appNames = [];
['apps', 'private'].forEach(function(dir) {
  var path = root + '/' + dir;
  if (!fs.existsSync(path)) { return; }
  fs.readdirSync(path, {withFileTypes: true}).forEach(function(e) {
    if (e.isDirectory() || e.isSymbolicLink()) { appNames.push(e.name); }
  });
});
check('the app list was actually found', appNames.length > 3);
var named = appNames.filter(function(name) {
  // Word-boundary, so a name that is also an ordinary word (an app called
  // "chat", a directory called "ui") does not fire on prose or on a CSS class.
  return new RegExp('\\b' + name.replace(/[^a-z0-9_]/gi, '') + '\\b', 'i').test(src);
});
check('names no app: ' + (named.join(', ') || 'none'), named.length === 0);

// A failed fetch hides the bell rather than showing an empty one. An empty bell
// claims "nothing to tell you", which is not something it can know when it
// could not ask.
check('an unreachable API hides the bell instead of claiming nothing happened',
  /catch\(function\(\) \{[\s\S]{0,400}?visibility = 'hidden'/.test(src));

check('a reachable API shows it', /visibility = 'visible'/.test(src));

// The count is the anti-spam feature, and stating "1 time" on every row would
// bury the rows that repeat.
check('the repeat count is shown only when there is one',
  /it\.count > 1/.test(src));

// Slower than the live pill: this answers "did anything happen", not "is
// something running", and it runs on every open tab.
check('it polls on its own slower timer', /setInterval\(load, 30000\)/.test(src));

process.exit(fail ? 1 : 0);
