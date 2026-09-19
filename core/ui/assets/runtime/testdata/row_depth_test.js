// A list whose items contain other items is an ordinary shape, so nesting is a
// property of a ROW: the server emits rows already in order and says how deep
// each one sits, and the layout does not have to learn what the nesting means.
//
// Indent only, deliberately. A box-drawing tree needs to know whether each
// ancestor has more siblings coming, which is a second model of the same data
// held in the renderer, and it is wrong the first time a row is filtered out of
// the middle.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('a row can declare its depth', /row\._depth/.test(src));

// Clamped at both ends. A negative depth would pull a card out of the list, and
// an unbounded one would push it off the right edge where no amount of
// scrolling finds it — both from a number the server could get wrong.
check('depth is clamped low and high',
  /Math\.max\(0, Math\.min\(6, parseInt\(row\._depth, 10\) \|\| 0\)\)/.test(src));

// A row that declares nothing must render exactly as it did before, or every
// existing cards view shifts.
check('no depth means no indent', /\(depth \? ';margin-left:'/.test(src));

process.exit(fail ? 1 : 0);
