// FormField type "scale": an ordered choice drawn as a track. The form builder
// is one large closure, so this checks the branch's source for the promises
// the Go side documents: stops in the order given, the chosen stop marked for
// assistive tech, its help line shown, and the value saved like a select.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}

var from = src.indexOf("} else if (t === 'scale') {");
var to = src.indexOf("} else if (t === 'toggle') {", from);
check('the form builder has a scale branch', from >= 0 && to > from);
var branch = src.slice(from, to);
check('stops are built from the options, in order', /\(f\.options \|\| \[\]\)\.forEach\(function\(o\)/.test(branch));
check('the track is a radio group of radio stops', /role: 'radiogroup'/.test(branch) && /role: 'radio'/.test(branch));
check('the chosen stop is marked aria-checked', /setAttribute\('aria-checked'/.test(branch));
check('the chosen stop\'s help is shown under the track', /about\.textContent = hit \? \(hit\.help/.test(branch));
check('a click saves the value like a select', /save\(f\.field, o\.value\)/.test(branch));
check('choosing the stop already chosen saves nothing', /if \(b\.classList\.contains\('active'\)\) return;/.test(branch));
check('suggest and presets can set it', /fieldSetters\[f\.field\] = function\(v\) \{ showScale\(v\); save\(f\.field, v\); \}/.test(branch));
check('on a phone the stops stack', /@media \(max-width: 600px\)\s*\{\s*\.ui-form-scale \{ flex-direction: column; \}/.test(css));

console.log(fail === 0 ? '\nALL SCALE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
