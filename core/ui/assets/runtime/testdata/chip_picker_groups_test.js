// A grouped attach list does not let one big group bury the rest. Nothing is
// folded away for it: the groups are listed with their counts at the top, the
// smaller groups come first, and a search narrows every group at once.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
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
eval(lift('function cpGroups(rows, groupOf, textOf, query)'));

var rows = [];
for (var i = 1; i <= 20; i++) rows.push({name: 'Agent ' + i, group: 'Agents', desc: 'an agent'});
rows.push({name: 'Cases', group: 'File stores', desc: 'Adds: search_cases'});
rows.push({name: 'Intake', group: 'File stores', desc: 'Adds: search_intake'});
rows.push({name: 'Triage', group: 'Guides', desc: 'how to read a dump'});
var groupOf = function(o){ return o.group; };
var textOf = function(o){ return o.name + ' ' + o.desc; };

var g = cpGroups(rows, groupOf, textOf, '');
check('every group is there', g.length === 3);
check('the smallest groups come first', g.map(function(x){ return x.label; }).join(',') === 'Guides,File stores,Agents',
  g.map(function(x){ return x.label + ':' + x.rows.length; }).join(','));
check('a group keeps its rows in the order they came', g[1].rows[0].name === 'Cases' && g[1].rows[1].name === 'Intake');

g = cpGroups(rows, groupOf, textOf, 'search intake');
check('a search needs every word, across the row', g.length === 1 && g[0].rows.length === 1 && g[0].rows[0].name === 'Intake');
g = cpGroups(rows, groupOf, textOf, 'file');
check('a search matches the group name too', g.length === 1 && g[0].label === 'File stores' && g[0].rows.length === 2);
check('nothing matching is no groups', cpGroups(rows, groupOf, textOf, 'zzz').length === 0);
g = cpGroups(rows, null, textOf, '');
check('an ungrouped list is one unnamed group', g.length === 1 && g[0].label === '' && g[0].rows.length === 23);

// The search box survives a redraw: adding a row redraws the rows under it,
// not the box, or every add would take the box and what was typed with it.
check('only the rows are redrawn', src.indexOf("listBody.innerHTML = '';") >= 0 && src.indexOf("listWrap.innerHTML = '';") < 0);
check('the groups are listed at the top when there is more than one', src.indexOf("toc = el('div', {class: 'ui-cp-toc'});") >= 0);

process.exit(fail ? 1 : 0);
