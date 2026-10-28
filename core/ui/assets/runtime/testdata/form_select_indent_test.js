// A select option marked indent sits under the one before it: a native option
// takes no styling, so the nesting is drawn in its text, while its value and
// the label anything reads back stay as given.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
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

function el(tag, attrs, kids) { return {tag: tag, value: attrs.value, text: (kids || []).join(''), selected: false}; }
var multi = false, selectedSet = {}, initial = 's1';
eval(lift('var mkOpt = function(o) {'));

var parent = mkOpt({value: 'p1', label: 'Parent'});
var child = mkOpt({value: 's1', label: 'Child', indent: 1});
check('a top-level option reads as its label', parent.text === 'Parent');
check('an indented one is drawn under it, marked', child.text === '   ↳ Child');
check('with its own value, and still selectable', child.value === 's1' && child.selected === true);
check('deeper is further in', mkOpt({value: 'x', label: 'Deeper', indent: 2}).text.indexOf('      ↳ ') === 0);

process.exit(fail ? 1 : 0);
