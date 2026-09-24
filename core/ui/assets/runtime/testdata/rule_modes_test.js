// Harness: the per-row mode helpers of a rules field (FormField.RowModes).
// A row's mode is the marker its line starts with; the picker owns it and the
// input shows the rest.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var from = src.indexOf('function uiRuleModeOf');
var to = src.indexOf('// end rule modes');
if (from < 0 || to < 0) { console.log('FAIL: helpers not found'); process.exit(1); }
var mod = new Function(src.slice(from, to) + '\nreturn {of: uiRuleModeOf, line: uiRuleModeLine};')();

var modes = [{marker: '', label: 'Block'}, {marker: '?', label: 'Attempt recovery'}, {marker: '~', label: 'Allow appeal'}];
var fail = 0;
function eq(name, got, want) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { fail++; console.log('FAIL ' + name + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
  else console.log('ok   ' + name);
}
eq('plain line is the default', mod.of('Do not quote prices', modes), {mode: 0, body: 'Do not quote prices'});
eq('marker read off', mod.of('? Keep it short', modes), {mode: 1, body: 'Keep it short'});
eq('marker without space', mod.of('~Never X', modes), {mode: 2, body: 'Never X'});
eq('one marker only, the rest stays visible', mod.of('? ~ Both', modes), {mode: 1, body: '~ Both'});
eq('compose default', mod.line(0, ' Do not quote prices ', modes), 'Do not quote prices');
eq('compose marker', mod.line(1, 'Keep it short', modes), '? Keep it short');
eq('empty body keeps the picked mode', mod.line(2, '', modes), '~');
eq('and reads back', mod.of('~', modes), {mode: 2, body: ''});
eq('round trip', mod.of(mod.line(2, 'Never X', modes), modes), {mode: 2, body: 'Never X'});
eq('switching mode rewrites the marker', mod.line(0, mod.of('? Keep it short', modes).body, modes), 'Keep it short');
console.log(fail === 0 ? '\nALL PASS' : '\n' + fail + ' FAILURES');
