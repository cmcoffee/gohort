// A filling frame (Frame.Fill) takes the room left below its own top, and
// gives it back: a phone keyboard opening shrinks it, closing grows it again,
// and it never drops under a usable 320px. The frame's markup carries the
// class the measurement looks for, so the two cannot drift apart.

var fs = require('fs');
var epi = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');
var misc = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

var from = epi.indexOf('  function fitFillFrames(vh) {');
var to = epi.indexOf('  // Coalesce bursts');
if (from < 0 || to < from) { console.log('FAIL fitFillFrames moved in 99_epilogue.js'); process.exit(1); }

var frame = {style: {}, top: 120, getBoundingClientRect: function () { return {top: this.top}; }};
var doc = {querySelectorAll: function (sel) { return sel === '.ui-frame-fill > iframe' ? [frame] : []; }};
var win = {scrollY: 0};
var fit = new Function('document', 'window', epi.slice(from, to) + '\nreturn fitFillFrames;')(doc, win);

fit(900);
check('it takes the room below its top', frame.style.height === '772px', frame.style.height);
fit(500);
check('a keyboard opening shrinks it', frame.style.height === '372px', frame.style.height);
fit(900);
check('closing the keyboard gives the room back', frame.style.height === '772px', frame.style.height);
fit(300);
check('it never drops under 320px', frame.style.height === '320px', frame.style.height);
win.scrollY = 200; frame.top = -80;
fit(900);
check('a scrolled page measures from the top of the document', frame.style.height === '772px', frame.style.height);

check('a filling frame is marked for the measurement', /class: 'ui-frame' \+ \(cfg\.fill \? ' ui-frame-fill' : ''\)/.test(misc));
check('every viewport sync fits filling frames', /fitFullHeightPanels\(vh\);\s*fitFillFrames\(vh\);/.test(epi));
check('a new filling frame asks for a measurement once it is on the page', /cfg\.fill && window\.requestAnimationFrame/.test(misc));

console.log(fail === 0 ? '\nALL FILL FRAME TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
