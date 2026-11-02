// An output field (readonly, link) with nothing in it is not shown: an empty
// box nobody can fill in says nothing. It is registered like any field, so
// its show_when applies, and a show_when that holds does not bring an empty
// one back.
var fs = require('fs');
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var src = basics.match(/    function hideIfEmpty\(node, empty\) \{[\s\S]*?\n    \}\n/);
check('hideIfEmpty is in the form code', !!src);
eval(src[0]);
var empty = {style: {}}, full = {style: {}};
hideIfEmpty(empty, true);
hideIfEmpty(full, false);
check('an empty one is hidden and marked', empty.style.display === 'none' && empty.uiEmpty === true);
check('one with a value is left as it is', full.style.display === undefined && full.uiEmpty === false);

var readonly = basics.match(/if \(t === 'readonly'\) \{([\s\S]*?)\n        return fieldWrap;\n      \}/);
check('a readonly field hides when empty and is registered',
  !!readonly && /hideIfEmpty\(fieldWrap, roVal === ''\);/.test(readonly[1]) && /fieldEls\[f\.field\] = fieldWrap;/.test(readonly[1]));
var link = basics.match(/if \(t === 'link'\) \{([\s\S]*?)\n        return fieldWrap;\n      \}/);
check('a link with nowhere to go hides and is registered',
  !!link && /hideIfEmpty\(fieldWrap, !href\);/.test(link[1]) && /fieldEls\[f\.field\] = fieldWrap;/.test(link[1]));
var vis = basics.match(/    function applyVisibility\(\) \{([\s\S]*?)\n    \}\n/);
check('show_when keeps an empty one hidden', !!vis && /!node\.uiEmpty && matchesShowWhen\(f\.show_when\)/.test(vis[1]));

if (fail) { console.log(fail + ' FAIL'); process.exit(1); }
