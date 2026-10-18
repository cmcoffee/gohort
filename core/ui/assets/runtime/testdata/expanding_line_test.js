// A list entry reads on one line and opens to its whole text while it is
// edited, or from its arrow, which shows only when there is more to see. An
// entry kept one per line never takes a newline.
var fs = require('fs');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var src = prelude.match(/  window\.uiExpandingLine = function\(ta, opts\) \{[\s\S]*?\n  \};\n/);
check('uiExpandingLine is in the prelude', !!src);

function listeners() {
  var on = {};
  return {
    on: on,
    addEventListener: function(name, fn) { (on[name] = on[name] || []).push(fn); },
    fire: function(name, ev) { (on[name] || []).forEach(function(fn) { fn(ev || {}); }); },
  };
}
function classes() {
  var set = {};
  return {
    add: function(c) { set[c] = true; },
    contains: function(c) { return !!set[c]; },
    toggle: function(c, on) { if (on) set[c] = true; else delete set[c]; },
  };
}

// A box 200px wide, 10px a character, 14px a line: what the browser would
// report for its text, width and state. Its value is an accessor on the
// prototype, as an element's is, so setting it from code can be watched.
var CHARS = 20, LINE = 14;
var textareaProto = {};
Object.defineProperty(textareaProto, 'value', {
  get: function() { return this._v; },
  set: function(v) { this._v = String(v); },
});
function fakeTextarea(value) {
  var l = listeners();
  var ta = Object.create(textareaProto);
  ta._v = value;
  var own = {
    rows: 2, wrap: 'soft', style: {height: ''}, classList: classes(),
    scrollLeft: 5, scrollTop: 5, selectionStart: 0, selectionEnd: 0, clientWidth: 200,
    addEventListener: l.addEventListener, fire: l.fire,
  };
  Object.keys(own).forEach(function(k) { ta[k] = own[k]; });
  function lines() { return ta.value.split('\n'); }
  Object.defineProperty(ta, 'scrollWidth', {get: function() {
    if (ta.wrap === 'soft') return ta.clientWidth;
    return Math.max(ta.clientWidth, Math.max.apply(null, lines().map(function(s) { return s.length * 10; })));
  }});
  Object.defineProperty(ta, 'scrollHeight', {get: function() {
    // Open, the rows set the least height, as a textarea's rows attribute does.
    if (ta.wrap === 'soft') return Math.max(ta.rows, lines().reduce(function(n, s) { return n + Math.max(1, Math.ceil(s.length / CHARS)); }, 0)) * LINE;
    return lines().length * LINE;
  }});
  Object.defineProperty(ta, 'clientHeight', {get: function() {
    return ta.classList.contains('is-open') ? (parseInt(ta.style.height, 10) || LINE) : LINE;
  }});
  return ta;
}

global.document = {createElement: function() {
  var l = listeners();
  var b = {style: {}, attrs: {}, addEventListener: l.addEventListener, fire: l.fire,
    setAttribute: function(k, v) { b.attrs[k] = v; }};
  return b;
}};
// The computed style a box reports; a test that needs a padded field sets its own.
global.window = {getComputedStyle: function(ta) {
  return ta.cs || {lineHeight: LINE + 'px', fontSize: '10px', paddingTop: '0px', paddingBottom: '0px',
    borderTopWidth: '0px', borderBottomWidth: '0px', boxSizing: 'border-box'};
}};
var timers = [];
global.setTimeout = function(fn) { timers.push(fn); };
function flush() { while (timers.length) timers.shift()(); }
eval(src[0]);

// Seen as the browser sees an icon button: ui-icon-btn sets display, which
// outranks the hidden attribute, so only the style can hide the arrow.
function shown(b) { return b.style.display !== 'none'; }

// A short entry: one line, nothing more to see.
var short = fakeTextarea('Be brief.');
var a = window.uiExpandingLine(short, {buttonClass: 'row-btn'});
flush();
check('it is one row tall, on the shared class', short.rows === 1 && short.classList.contains('ui-xline'));
check("the arrow takes the caller's class", a.className === 'row-btn' && a.type === 'button');
check('a short entry has no arrow', !shown(a) && !short.classList.contains('is-open'));
check('closed, the line starts at the start', short.scrollLeft === 0 && short.scrollTop === 0);

// A long one: closed it has an arrow, editing opens it to its whole text.
var long = fakeTextarea('Keep every reply under three short paragraphs unless the person asks for more.');
var b = window.uiExpandingLine(long, {});
flush();
check('the arrow defaults to the icon button', b.className === 'ui-icon-btn');
check('a long entry shows the arrow, pointing down', shown(b) && b.textContent === '⌄' && b.attrs['aria-expanded'] === 'false');
long.fire('focus');
check('editing opens it, wrapped, as tall as its text', long.classList.contains('is-open') && long.wrap === 'soft' && long.style.height === (Math.ceil(long.value.length / CHARS) * LINE) + 'px' && long.style.height !== LINE + 'px');
check('open, the arrow closes it', shown(b) && b.textContent === '⌃' && b.attrs['aria-expanded'] === 'true');
long.fire('blur');
check('leaving the box closes it to one line', !long.classList.contains('is-open') && long.wrap === 'off' && long.style.height === LINE + 'px');
check('and its arrow is still there to open it', shown(b) && b.textContent === '⌄');

// From the arrow: opened to read, it stays open when the box loses focus.
var pressed = false;
b.fire('mousedown', {preventDefault: function() { pressed = true; }});
check('pressing the arrow keeps the focus where it is', pressed);
b.fire('click');
check('the arrow opens it', long.classList.contains('is-open'));
long.fire('blur');
check('opened from the arrow, it stays open', long.classList.contains('is-open'));
b.fire('click');
check('and the arrow closes it again', !long.classList.contains('is-open'));

// One stored line per entry: no newline gets in, typed or pasted.
var rule = fakeTextarea('One rule');
var got = null;
window.uiExpandingLine(rule, {oneLine: true, onInput: function(v) { got = v; }});
var refused = false;
rule.fire('keydown', {key: 'Enter', preventDefault: function() { refused = true; }});
check('Enter is refused on a one-line entry', refused);
rule.value = 'One rule\n  and its tail';
rule.selectionStart = rule.value.length;
rule.fire('input');
check('a pasted break becomes a space', rule.value === 'One rule and its tail');
check('the caller sees the folded text', got === 'One rule and its tail');
check('the caret stays inside the text', rule.selectionStart <= rule.value.length);

// An entry that may hold lines keeps them, and closed shows only the first.
var note = fakeTextarea('First line');
var c = window.uiExpandingLine(note, {});
flush();
var kept = true;
note.fire('keydown', {key: 'Enter', preventDefault: function() { kept = false; }});
check('Enter is a newline where entries may hold lines', kept);
note.value = 'First line\nsecond';
note.fire('input');
check('the newline stays', note.value === 'First line\nsecond');
check('closed, a second line counts as more to see', shown(c));

// A form field opens to at least its usual rows, so a short answer still
// reads as room to write; closed it is one line again.
var field = fakeTextarea('Short.');
window.uiExpandingLine(field, {openRows: 3});
flush();
field.fire('focus');
check('a form field opens to at least its usual rows', field.rows === 3 && field.style.height === (3 * LINE) + 'px');
field.fire('blur');
check('and closes to one row', field.rows === 1);

// One line measured off the box, padding and border included: every box is
// sized border-box, so a height in em alone would clip the text of a padded
// form field.
var padded = fakeTextarea('A padded field');
padded.cs = {lineHeight: '19.6px', fontSize: '14px', paddingTop: '8.8px', paddingBottom: '8.8px',
  borderTopWidth: '1px', borderBottomWidth: '1px', boxSizing: 'border-box'};
window.uiExpandingLine(padded, {});
flush();
check('closed, a padded box is one line plus its padding and border', padded.style.height === Math.ceil(19.6 + 17.6 + 2) + 'px');

// A value set from code, a suggestion or a preset, fires no input event; the
// box still learns there is more to see.
var suggested = fakeTextarea('');
var sArrow = window.uiExpandingLine(suggested, {});
flush();
check('an empty box has no arrow', !shown(sArrow));
suggested.value = 'A suggestion long enough to run past the end of the line.';
check('a value set from code shows the arrow', shown(sArrow));
check('and the box still holds it', suggested.value === 'A suggestion long enough to run past the end of the line.');

// The form's inline boxes use it; a long document keeps its preview instead.
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var branch = basics.match(/if \(useExpand\) \{[\s\S]*?\n        \} else \{([\s\S]*?)\n        \}\n      \} else if \(t === 'select'\)/);
check('an inline form textarea goes through uiExpandingLine at its rows',
  !!branch && /window\.uiExpandingLine\(input, \{openRows: f\.rows \|\| 3\}\)/.test(branch[1]));
check('its row is what the field appends', /fieldWrap\.appendChild\(xlineRow \|\| input\)/.test(basics));

if (fail) { console.log(fail + ' FAIL'); process.exit(1); }
