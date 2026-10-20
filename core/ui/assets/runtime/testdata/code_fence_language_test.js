// A fenced block's language ("```text", "```json") is not part of its code.
// The renderer kept the whole opening line, so every diagram a model drew in a
// ```text block opened with the word "text". This drives the prelude's real
// mdToHTML.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');

var fail = 0;
function check(label, cond, got) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label + '\n  got ' + JSON.stringify(got)); }
}
function lift(start) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + start + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}
var window = {};
eval(lift('function mdToHTML('));

var fence = '```';
var out = mdToHTML('A diagram:\n\n' + fence + 'text\n+---+\n| a |\n+---+\n' + fence);
check('the language is not drawn as the first line', out.indexOf('<pre><code>+---+') >= 0 && out.indexOf('text\n+') < 0, out);
out = mdToHTML(fence + 'json\n{"a": 1}\n' + fence);
check('any language word is dropped', out.indexOf('<pre><code>{') >= 0, out);
out = mdToHTML(fence + '\nplain\n' + fence);
check('a fence with no language keeps its code', out.indexOf('<pre><code>plain') >= 0, out);
out = mdToHTML(fence + 'npm install' + fence);
check('a one-line fence keeps its code', out.indexOf('<pre><code>npm install</code></pre>') >= 0, out);
out = mdToHTML(fence + 'echo hi there\nmore\n' + fence);
check('code on the opening line is not taken for a language', out.indexOf('echo hi there') >= 0, out);

console.log(fail === 0 ? '\nALL FENCE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
