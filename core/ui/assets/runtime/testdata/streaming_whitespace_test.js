// A reply still arriving shows no blank space above or below its text.
//
// The streaming body is pre-wrap, so whitespace the markdown pass drops at the
// end was drawn while the reply arrived: a reply opening with blank lines sat
// under a gap, and a paragraph break, which arrives before the paragraph it
// opens, left a gap trailing the text. Both disappeared when the turn settled,
// so the reply jumped. This drives the panel's real streamingText over the
// prelude's real uiStripMetaTags.
var fs = require('fs');
var dir = __dirname + '/..';

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

function lift(src, start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}

var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

var window = {};
eval(lift(prelude, 'window.uiStripMetaTags = function', 'uiStripMetaTags'));
eval(lift(panel, 'function streamingText(', 'streamingText'));

check('blank lines before the reply are not drawn',
  streamingText('\n\n  \nThe answer is here.') === 'The answer is here.');
check('a paragraph break waiting for its paragraph is not drawn',
  streamingText('First paragraph.\n\n') === 'First paragraph.');
check('the break shows once the next paragraph arrives',
  streamingText('First paragraph.\n\nSecond') === 'First paragraph.\n\nSecond');
check('indentation on the first line survives (a code block can open a reply)',
  streamingText('\n    indented code') === '    indented code');
check('an internal note stripped from the front leaves no gap',
  streamingText('<gohort-meta>note</gohort-meta>\n\nVisible.') === 'Visible.');
check('nothing visible reads as empty',
  streamingText('\n\n ') === '' && streamingText('') === '' && streamingText(undefined) === '');

// Every path that draws in-progress text goes through it, and the end of the
// turn judges emptiness the same way, so blank lines alone never leave a card.
var append = lift(panel, 'function appendChunk(', 'appendChunk');
var replace = lift(panel, 'function replaceChunk(', 'replaceChunk');
// The revealer paints through showVisible, which trims and judges emptiness.
var show = lift(panel, 'function showVisible(', 'showVisible');
check('appending a chunk draws through showStreaming', /showStreaming\(m\)/.test(append));
check('replacing the text draws through showVisible', /showVisible\(m, m\.rawText\)/.test(replace));
check('showVisible draws the trimmed text and hides an empty bubble',
  /streamingText\(prefix\)/.test(show) && /markEmptyBubble\(m\)/.test(show));
var finalize = lift(panel, 'function finalizeMessage(', 'finalizeMessage');
check('a settled reply of blank lines stays hidden',
  /streamingText\(m\.rawText\)\.length > 0\) unmarkEmptyBubble/.test(finalize));
check('the markdown pass still gets every character',
  /uiRenderMarkdown\(m\.body, m\.rawText \|\| ''\)/.test(finalize));

process.exit(fail ? 1 : 0);
