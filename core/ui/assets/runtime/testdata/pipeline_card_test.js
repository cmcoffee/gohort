// A "card" block draws a stage's fields as values, laid out by the stage's
// card map. Drives the real helpers from 10_basics.js over a stand-in DOM.
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

var basics = fs.readFileSync(dir + '/10_basics.js', 'utf8');
var panel = fs.readFileSync(dir + '/40_pipeline_panel.js', 'utf8');

// A stand-in DOM: enough to read back what was drawn.
function node(tag, attrs, kids) {
  var n = {tag: tag, attrs: attrs || {}, kids: [], textContent: ''};
  n.appendChild = function(c) { n.kids.push(c); return c; };
  (kids || []).forEach(function(k) { n.appendChild(k); });
  return n;
}
function el(tag, attrs, kids) { return node(tag, attrs, kids); }
function text(n) {
  if (typeof n === 'string') return n;
  return (n.textContent || '') + n.kids.map(text).join(' ');
}
function find(n, cls) {
  if (typeof n === 'string') return null;
  if ((n.attrs.class || '') === cls) return n;
  for (var i = 0; i < n.kids.length; i++) { var f = find(n.kids[i], cls); if (f) return f; }
  return null;
}
var window = {};

eval(['function cardFieldText(', 'function cardLabel(', 'function cardAccent(', 'function cardMarkdown(', 'function fillCard(']
  .map(function(f) { return lift(basics, f, f); }).join('\n'));

var box = node('div');
var accent = fillCard(box,
  {title: 'verdict', badges: 'confidence, winning_side', body: 'reasoning', accent: 'winning_side'},
  {verdict: 'Ship it', confidence: 'high', winning_side: 'for', reasoning: 'Tests pass.', risks: ['latency', 'cost'], notes: ''},
  false);

check('the title field is the headline', text(find(box, 'ui-pl-card-title')) === 'Ship it');
var badges = find(box, 'ui-pl-card-badges');
check('badges show their label and value', badges && /Confidence\s+high/.test(text(badges)) && /Winning side\s+for/.test(text(badges)));
check('the body field is the main text', text(find(box, 'ui-pl-card-body')) === 'Tests pass.');
var rest = find(box, 'ui-pl-card-fields');
check('fields not placed are listed, labelled', rest && /Risks/.test(text(rest)) && /latency/.test(text(rest)) && /cost/.test(text(rest)));
check('an empty field is left out', !/Notes/.test(text(rest)));
check('placed fields are not listed twice', !/Reasoning/.test(text(rest)) && !/Verdict/.test(text(rest)));
check('the accent comes from the accent field', accent === cardAccent('for') && accent >= 0 && accent < 6);

check('the same value always gets the same colour', cardAccent('Against') === cardAccent('against ') && cardAccent('x') === cardAccent('x'));
check('no value, no accent', cardAccent('') === -1 && cardAccent(null) === -1);
check('a list reads as one line', cardFieldText(['a', 'b']) === 'a, b');
check('a list body reads as bullets', cardMarkdown(['a', 'b']) === '- a\n- b');
check('labels read as words', cardLabel('winning_side') === 'Winning side');

var bare = node('div');
fillCard(bare, {}, {score: 7, summary: 'fine'}, false);
check('with no layout, every field is listed', /Score/.test(text(bare)) && /Summary/.test(text(bare)));

// The panel registers the renderer, draws stored fields at once, and takes
// live ones through block_meta.
var card = lift(panel, 'blockRenderers.card = function(', 'the card renderer');
check('the panel draws card blocks', /fillCard\(box, d\.card \|\| \{\}, fields, cfg\.markdown\)/.test(card));
check('a stored run draws its fields straight away', /draw\(d\.fields\);/.test(card));
check('live fields arrive through block_meta', /if \(meta\.fields\) draw\(meta\.fields\);/.test(card));
check('the text rendering gives way to the values', /body\.style\.display = 'none'/.test(card));

process.exit(fail ? 1 : 0);
