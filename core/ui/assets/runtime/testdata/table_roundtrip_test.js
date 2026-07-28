var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../05_form_sections.js', 'utf8');
var cut = src.indexOf('function buildSectionsEditor');
var m = new Function(src.slice(0, cut) +
  '\nreturn {p:uiParseSections,s:uiSerializeSections,st:uiSectionState,mode:uiInferSectionMode,tbl:uiSectionTable,split:uiSplitRow};')();
function rt(t){ return m.s(m.p(t).map(function(r){ return m.st(r, null, true); })); }
var fail = 0;
function eq(label, got, want) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { fail++; console.log('FAIL ' + label + '\n  got  ' + JSON.stringify(got) + '\n  want ' + JSON.stringify(want)); }
  else console.log('ok   ' + label);
}

// --- detection ---
eq('detects a table', m.mode(['| a | b |','|---|---|','| 1 | 2 |']), 'table');
eq('detects aligned table', m.mode(['| a | b |','| :--- | ---: |','| 1 | 2 |']), 'table');
eq('one-column is not a table', m.mode(['| a |','|---|']), 'prose');
eq('pipes without separator are prose', m.mode(['a | b','c | d']), 'prose');
eq('bullets still list', m.mode(['- a','- b']), 'list');

// --- cell splitting ---
eq('escaped pipe stays one cell', m.split('| a \\| b | c |'), ['a | b','c']);
eq('no outer pipes', m.split('a | b | c'), ['a','b','c']);

// --- round-trip stability ---
var cases = {
  'plain table': '## Config\n\n| Setting | Default |\n|---|---|\n| port | 8080 |\n| host | local |',
  'aligned table': '## T\n\n| L | C | R |\n| :--- | :---: | ---: |\n| 1 | 2 | 3 |',
  'header only': '## T\n\n| A | B |\n|---|---|',
  'ragged row padded': '## T\n\n| A | B | C |\n|---|---|---|\n| 1 |',
  'escaped pipe survives': '## T\n\n| Expr | Note |\n|---|---|\n| a \\| b | or |',
  'table beside prose section': 'Intro text.\n\n## Table\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n## After\n\n- bullet',
};
Object.keys(cases).forEach(function(k) {
  var once = rt(cases[k]), twice = rt(once);
  if (once !== twice) { fail++; console.log('UNSTABLE ' + k + '\n  1: ' + JSON.stringify(once) + '\n  2: ' + JSON.stringify(twice)); }
  else console.log('ok   stable: ' + k + '  -> ' + JSON.stringify(once).slice(0, 70));
});

// alignment must survive verbatim
var a = rt('## T\n\n| L | C | R |\n| :--- | :---: | ---: |\n| 1 | 2 | 3 |');
if (a.indexOf(':---') < 0 || a.indexOf(':---:') < 0 || a.indexOf('---:') < 0) { fail++; console.log('FAIL alignment lost: ' + JSON.stringify(a)); }
else console.log('ok   alignment preserved');

// a literal pipe must come back as a literal pipe
var e = rt('## T\n\n| Expr | Note |\n|---|---|\n| a \\| b | or |');
if (e.indexOf('a \\| b') < 0) { fail++; console.log('FAIL escaped pipe lost: ' + JSON.stringify(e)); }
else console.log('ok   escaped pipe preserved');

console.log(fail === 0 ? '\nALL TABLE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
