// A fenced block's language ("```text", "```json") is not part of its code,
// and nothing inside code is read as markdown.
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


// Nothing inside code is read as markdown. A shell script's comment lines
// were drawn as headings with their # gone, its globs as italics, and a blank
// line in it split the block in two.
var script = '#!/bin/bash\n# back up the logs\nfor f in *.log *.txt; do\n  - not a list\n  echo **$f**\n\n  cp "$f" /tmp/https://x\ndone\n';
out = mdToHTML('Run this:\n\n' + fence + 'bash\n' + script + fence + '\n\nThen check it.');
check('a script comes back as written, in one block',
  out.indexOf('<pre><code>' + script.replace(/&/g, '&amp;').replace(/"/g, '&quot;') + '</code></pre>') >= 0, out);
check('its comment is not a heading', out.indexOf('<h1>') < 0, out);
check('nothing in it is italic, bold, a list or a link',
  out.indexOf('<em>') < 0 && out.indexOf('<strong>') < 0 && out.indexOf('<li>') < 0 && out.indexOf('<a ') < 0, out);
check('the prose around it is still prose', out.indexOf('<p>Run this:</p><pre>') >= 0 && out.indexOf('</pre><p>Then check it.</p>') >= 0, out);
out = mdToHTML('Set `*.go` and `**/x` but **bold** stays');
check('inline code is verbatim too, markdown beside it still renders',
  out.indexOf('<code>*.go</code>') >= 0 && out.indexOf('<code>**/x</code>') >= 0 && out.indexOf('<strong>bold</strong>') >= 0, out);
out = mdToHTML('## Use `make`\n\n- run `make test`');
check('code inside a heading or a list item is put back in place',
  out.indexOf('<h2>Use <code>make</code></h2>') >= 0 && out.indexOf('<li>run <code>make test</code></li>') >= 0, out);
out = mdToHTML('a \uE0000\uE001 b');
check('a stray placeholder character in the text cannot pull code in', out === '<p>a 0 b</p>', out);

console.log(fail === 0 ? '\nALL FENCE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
