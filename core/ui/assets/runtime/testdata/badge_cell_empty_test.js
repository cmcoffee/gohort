// A badge that is off leaves its cell with no children at all, so the
// stylesheet's :empty rule drops the cell and the gap it would cost the row.
// One that is on carries its pill.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var start = src.indexOf('function renderBadgeCell(');
var end = src.indexOf('\n  }\n', start);
if (start < 0 || end < 0) { console.log('FAIL renderBadgeCell not found'); process.exit(1); }
function el(tag, attrs, kids) {
  var n = {tag: tag, className: (attrs && attrs.class) || '', style: {}, childNodes: []};
  n.appendChild = function(c) { n.childNodes.push(c); return c; };
  (kids || []).forEach(function(k) { if (k != null) n.appendChild(typeof k === 'string' ? {text: k} : k); });
  return n;
}
var renderBadgeCell = new Function('el', src.slice(start, end + 4) + '\nreturn renderBadgeCell;')(el);
var fail = 0;
function check(label, ok) { if (!ok) { fail++; console.log('FAIL ' + label); } else console.log('ok   ' + label); }

var col = {field: 'disabled', type: 'badge', badges: [{value: true, label: 'Disabled', color: 'danger'}]};
[false, null, '', undefined].forEach(function(v) {
  var off = renderBadgeCell(col, v);
  check('an off badge (' + JSON.stringify(v) + ') leaves an empty cell', off.childNodes.length === 0 && /ui-table-cell-badge/.test(off.className));
});
var on = renderBadgeCell(col, true);
check('an on badge carries its pill', on.childNodes.length === 1 && /ui-badge danger/.test(on.childNodes[0].className));

console.log(fail === 0 ? '\nALL BADGE CELL TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
