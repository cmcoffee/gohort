// ToggleGroup posts only the switch that moved.
//
// It posted the whole record it loaded with the page. A form beside it on the
// same record (a master switch over a name field) saved a new name, then
// flipping the switch wrote the page-load copy of the name back.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var i = src.indexOf('components.toggle_group = function');
var block = i >= 0 ? src.slice(i, i + 1600) : '';
check('the toggle group is found', block.length > 0);
check('a flip sends only its own field',
  /var change = \{\};\s*\n\s*change\[t\.field\] = input\.checked;[\s\S]{0,200}body: JSON\.stringify\(change\)/.test(block));
check('the whole loaded record is never posted', block.indexOf('JSON.stringify(current)') < 0);

if (fail) { console.log(fail + ' failed'); process.exit(1); }
console.log('all passed');
