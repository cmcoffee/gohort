// Harness: pull the pure functions out of 05_form_sections.js (no DOM).
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../05_form_sections.js', 'utf8');
var cut = src.indexOf('function buildSectionsEditor');
var mod = new Function(src.slice(0, cut) + '\nreturn {p:uiParseSections,s:uiSerializeSections,m:uiInferSectionMode,i:uiSectionItems,st:uiSectionState};')();

function roundTrip(text, specs) {
  var parsed = mod.p(text);
  var secs = parsed.map(function(r){ return mod.st(r, null, true); });
  return mod.s(secs);
}
var cases = {
  'free-form legacy prompt': "You are a research helper.\nAlways cite sources.",
  'headed doc': "## Role\nYou are a helper.\n\n## Rules\n- cite a URL\n- never guess prices\n\n## Steps\n1. restate the goal\n2. gather\n3. synthesize",
  'intro + headings': "Preamble text here.\n\n## Rules\n- one\n- two",
  'mixed body stays prose': "## Notes\n- a bullet\nand a bare line\n- another",
  'h1 headings': "# Role\nsomething\n\n# Rules\n- x",
  'numbered with parens': "## Steps\n1) first\n2) second",
  'star bullets': "## Rules\n* alpha\n* beta",
  'empty': "",
  'trailing whitespace': "## Role\nhello   \n\n\n",
  'deep nesting': "## Rules\n### Hard\n- no\n### Soft\n- maybe",
};
var fail = 0;
Object.keys(cases).forEach(function(k) {
  var once = roundTrip(cases[k]);
  var twice = roundTrip(once);
  var stable = once === twice;
  if (!stable) { fail++; console.log('UNSTABLE  ' + k + '\n  1st: ' + JSON.stringify(once) + '\n  2nd: ' + JSON.stringify(twice)); }
  else console.log('stable    ' + k + '  -> ' + JSON.stringify(once).slice(0,72));
});
// content preservation: every non-blank source line must survive (modulo markers)
Object.keys(cases).forEach(function(k) {
  var out = roundTrip(cases[k]);
  cases[k].split('\n').forEach(function(l) {
    var t = l.trim().replace(/^[-*]\s+/,'').replace(/^\d+[.)]\s+/,'').replace(/^#+\s+/,'');
    if (t === '') return;
    if (out.indexOf(t) === -1) { fail++; console.log('LOST TEXT in ' + k + ': ' + JSON.stringify(t)); }
  });
});
console.log(fail === 0 ? '\nALL PASS' : '\n' + fail + ' FAILURES');

// Placeholder slots must contribute nothing until filled.
var ph = mod.s([
  mod.st({title:'Role', level:2, lines:['You are a helper.']}, {title:'Role'}, true),
  mod.st({title:'Voice', level:2, lines:[]}, {title:'Voice', mode:'prose'}, false),
]);
console.log('placeholder skipped: ' + (ph === '## Role\nYou are a helper.' ? 'PASS' : 'FAIL -> ' + JSON.stringify(ph)));

// A free section added but left blank contributes nothing.
var blank = mod.s([mod.st({title:'', level:2, lines:[]}, null, false)]);
console.log('blank free section skipped: ' + (blank === '' ? 'PASS' : 'FAIL -> ' + JSON.stringify(blank)));
process.exit(fail ? 1 : 0);
