// uiSafeURL: a URL that would run script when followed or loaded becomes "#",
// read the way a browser reads a scheme; everything else passes unchanged.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var m = src.match(/  function safeURL\(u\) \{[\s\S]*?\n  \}\n/);
if (!m) { console.log('FAIL could not find safeURL()'); process.exit(1); }
var safeURL = new Function(m[0] + '\nreturn safeURL;')();
var fail = 0;
function check(u, want) {
  var got = safeURL(u);
  if (got !== want) { fail++; console.log('FAIL ' + JSON.stringify(u) + ' -> ' + JSON.stringify(got)); }
  else console.log('ok   ' + JSON.stringify(u));
}
['javascript:alert(1)', 'JavaScript:x', ' java\tscript:x', '\u0001javascript:x', 'jav\nascript:x', 'vbscript:x'].forEach(function(u) { check(u, '#'); });
['https://ok.example/', '/rel/path', 'data:image/png;base64,AA', '#frag', 'mailto:a@b', 'records?id=1'].forEach(function(u) { check(u, u); });
console.log(fail ? fail + ' FAILURES' : '\nALL SAFE-URL TESTS PASS');
process.exit(fail ? 1 : 0);
