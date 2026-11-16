// A DisplayPair with hide_empty leaves its row out while the value is empty,
// and shows it when there is something to say; a pair without it keeps its
// row either way.
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
function el(tag, attrs, kids) {
  return {tag: tag, attrs: attrs || {}, kids: kids || [], appendChild: function(c) { this.kids.push(c); }};
}
function lookup(d, f) { return d[f]; }
function fmt(v) { return v == null ? '' : String(v); }
eval(lift(prelude, 'function uiDisplayPair(wrap, data, p)', 'uiDisplayPair'));

var wrap = el('div');
uiDisplayPair(wrap, {why: ''}, {label: 'Updates here', field: 'why', hide_empty: true});
check('an empty hide_empty pair adds no row', wrap.kids.length === 0);
uiDisplayPair(wrap, {}, {label: 'Updates here', field: 'why', hide_empty: true});
check('a missing hide_empty pair adds no row', wrap.kids.length === 0);
uiDisplayPair(wrap, {why: 'Not offered: a private build'}, {label: 'Updates here', field: 'why', hide_empty: true});
check('a hide_empty pair with a value adds its row', wrap.kids.length === 1);
uiDisplayPair(wrap, {why: ''}, {label: 'Other', field: 'why'});
check('a pair without it keeps its row', wrap.kids.length === 2);
process.exit(fail ? 1 : 0);
