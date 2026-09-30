// The isolated frame's fetch bridge lets an app's page reach only the app's
// own endpoints. The check is pulled out of 70_misc.js and run against the
// spellings a browser normalizes before it fetches.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');
var m = src.match(/    function permitted\(u\) \{[\s\S]*?\n    \}\n/);
if (!m) { console.log('FAIL could not find permitted()'); process.exit(1); }
var make = new Function('allowed', 'location', m[0] + '\nreturn permitted;');
var loc = {href: 'https://site.example/apps/sample/', origin: 'https://site.example'};
var permitted = make(['data/', 'action/', 'records', 'assets/'], loc);

var fail = 0;
function check(u, want) {
  var got = permitted(u);
  if (got !== want) { fail++; console.log('FAIL ' + JSON.stringify(u) + ' permitted=' + got + ' want ' + want); }
  else console.log('ok   ' + JSON.stringify(u) + ' ' + got);
}
check('data/items', true);
check('action/close?id=3', true);
check('records', true);
check('assets/logo.png', true);
check('data/../../other/api/approve', false);
check('data/%2e%2e/%2e%2e/other/api/approve', false);
check('data/.%2E/x', false);
check('data\\..\\..\\other', false);
check('data/x%2f..%2f..%2fadmin', false);
check('/other/api/approve', false);
check('//evil.example/x', false);
check('https://site.example/admin', false);
check('javascript:alert(1)', false);
check('settings', false);
console.log(fail ? fail + ' FAILURES' : '\nALL ISOLATE-FETCH TESTS PASS');
process.exit(fail ? 1 : 0);
