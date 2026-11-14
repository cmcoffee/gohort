// A rows field that holds a list of cases: the first row a different kind
// from the rest (FirstDefault / Default), "+ Add" greyed while a row closes
// the list (AddBlockedWhen, with its note), and a cell that holds a whole
// document, previewed and edited in a modal with the Markdown / Raw views
// (uiOpenDocEditor). The pieces are lifted out of the runtime and run.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var sections = fs.readFileSync(__dirname + '/../05_form_sections.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function liftBody(text, head) {
  var start = text.indexOf(head);
  if (start < 0) throw new Error('missing: ' + head);
  var depth = 0, i = text.indexOf('{', start);
  for (; i < text.length; i++) {
    if (text[i] === '{') depth++;
    else if (text[i] === '}' && --depth === 0) break;
  }
  return text.slice(start, i + 1);
}
var matchesWhen = new Function(liftBody(src, 'function hasValue(') + '\n' + liftBody(src, 'function matchesWhen(') + '\nreturn matchesWhen;')();

// "+ Add" is greyed while any row matches, and says why.
var sync = liftBody(src, 'syncAddBlocked = function() {\n');
var f = {add_blocked_when: 'kind:else', add_blocked_note: 'The Else is last.'};
var addBtn = {}, addNote = {style: {}};
var rowsVal = [{kind: 'if'}, {kind: 'else_if'}];
var syncAddBlocked;
eval(sync);
syncAddBlocked();
check('add is open while no row closes the list', !addBtn.disabled && addNote.style.display === 'none');
rowsVal.push({kind: 'else'});
syncAddBlocked();
check('add is greyed once an Else is there', addBtn.disabled === true);
check('and says what to change, on the button and under it', addBtn.title === 'The Else is last.' && addNote.textContent === 'The Else is last.' && addNote.style.display === '');
rowsVal[2].kind = 'else_if';
syncAddBlocked();
check('changing the Else to Else if opens it again', !addBtn.disabled && addBtn.title === '');
f = {};
rowsVal = [{kind: 'else'}];
syncAddBlocked();
check('a rows field with no add_blocked_when is never greyed', !addBtn.disabled);
check('the check runs on every redraw and every save',
  /function drawRows\(\) \{\s*body\.innerHTML = '';\s*syncAddBlocked\(\);/.test(src) &&
  /save\(f\.field, clean\);\s*syncAddBlocked\(\);/.test(src));
check('a greyed add does nothing when pressed', /if \(addBtn\.disabled\) return;/.test(src));

// A value set from outside (Suggest) replaces the rows, as an array or a
// JSON string of one, and is saved.
var setter = src.slice(src.indexOf('fieldSetters[f.field] = function(v) {\n          var next = v;'));
setter = setter.slice(0, setter.indexOf('\n        };') + 11);
var fieldSetters = {}, saves = [], drawn = 0;
f = {field: 'conditions'};
rowsVal = [{kind: 'if'}];
function drawRows() { drawn++; }
function persistRows() { saves.push(JSON.stringify(rowsVal)); }
eval(setter);
fieldSetters.conditions('[{"kind":"if","question":"q"},{"kind":"else"}]');
check('a JSON string of rows replaces them and saves', rowsVal.length === 2 && rowsVal[1].kind === 'else' && saves.length === 1 && drawn === 1);
fieldSetters.conditions([{kind: 'if'}]);
check('so does an array', rowsVal.length === 1 && saves.length === 2);
fieldSetters.conditions('not json');
fieldSetters.conditions({kind: 'if'});
check('anything else changes nothing', rowsVal.length === 1 && saves.length === 2);

// The first row takes first_default, later rows default.
check('a new first row takes first_default, a later one default',
  /var first = rowsVal\.length === 0;/.test(src) &&
  /if \(first && c\.first_default\) blank\[c\.field\] = c\.first_default;\s*else if \(c\.default\) blank\[c\.field\] = c\.default;/.test(src));

// A document cell: a preview, and Edit into the modal editor, whose Save
// commits the row.
var cell = src.slice(src.indexOf("} else if (c.type === 'sections') {"), src.indexOf("} else if (c.type === 'textarea') {"));
check('a sections cell previews its text and opens the doc editor',
  /ui-rows-doc-preview/.test(cell) && /window\.uiOpenDocEditor\(/.test(cell));
check('saving in the editor writes the row and commits it',
  /onSave: function\(text\) \{ docVal = text; row\[c\.field\] = text; showDocPrev\(\); commit\(\); \}/.test(cell));
check('the preview is a few lines, not the document',
  /\.ui-rows-doc-preview \{[^}]*max-height: calc\(3 \* 1lh\); overflow: hidden; animation: ui-cut-fade-y/.test(css));

// uiOpenDocEditor: Markdown and Raw, Save hands back the open view's text,
// Cancel hands back nothing.
function node(tag) {
  var n = {tag: tag, children: [], style: {}, value: '', textContent: '', listeners: {},
    classList: {set: {}, add: function(c) { this.set[c] = true; }, remove: function(c) { delete this.set[c]; },
      contains: function(c) { return !!this.set[c]; }},
    appendChild: function(c) { this.children.push(c); },
    addEventListener: function(ev, fn) { this.listeners[ev] = fn; }};
  return n;
}
function el(tag, opts, kids) {
  var n = node(tag);
  if (opts && opts.class) opts.class.split(' ').forEach(function(c) { n.classList.add(c); });
  (kids || []).forEach(function(k) { if (typeof k === 'string') n.textContent += k; else n.children.push(k); });
  return n;
}
var editorValue = '';
function buildSectionsEditor(opts) {
  editorValue = opts.initial;
  return {node: node('div'), getValue: function() { return editorValue; }, setValue: function(v) { editorValue = v; }};
}
var modal = null;
var window = {uiOpenModal: function(o) { modal = o; var body = node('div'); o.mount(body); modal.body = body; }};
eval(liftBody(sections, 'function uiOpenDocEditor(opts) {'));
window.uiOpenDocEditor = uiOpenDocEditor;
var saved = [];
function open(value) {
  saved = [];
  window.uiOpenDocEditor({title: 'Instructions', value: value, onSave: function(t) { saved.push(t); }});
  var row = modal.body.children[0];
  return {outline: row.children[0], raw: row.children[1], ta: modal.body.children[2]};
}
var v = open('## Steps\n\nClear the cache.');
check('it opens in the Markdown view, so labelled', v.outline.classList.contains('on') && v.outline.textContent === 'Markdown' && v.ta.style.cssText.indexOf('display:none') >= 0);
editorValue = '## Steps\n\nClear the cache, then sign in again.';
modal.actions[1].onClick({close: function() {}});
check('Save from the Markdown view hands back its text', saved.length === 1 && saved[0].indexOf('sign in again') >= 0);
v = open('first');
v.raw.listeners.click();
v.ta.value = 'typed raw';
modal.actions[1].onClick({close: function() {}});
check('Save from Raw hands back the raw text', saved.length === 1 && saved[0] === 'typed raw');
v = open('kept');
check('Cancel saves nothing', !modal.actions[0].onClick && saved.length === 0);

// An empty document opens with a box to write in, and an untouched box
// still saves as nothing.
var pure = new Function(sections.slice(0, sections.indexOf('function uiOpenDocEditor')) + '\nreturn {s:uiSerializeSections,st:uiSectionState};')();
check('an empty document starts with one untitled box', /if \(!secs\.length\) secs\.push\(uiSectionState\(\{title: '', level: 0, lines: \[\]\}, null, false\)\);/.test(sections));
check('an untouched box saves as nothing', pure.s([pure.st({title: '', level: 0, lines: []}, null, false)]) === '');
check('alone, the box is labelled Text, not Unsectioned', /\[alone \? 'Text' : 'Unsectioned'\]/.test(sections));

// A required field says so on its label, and a placeholder reads as a hint.
check('a required field is marked on its label', /if \(f\.required\) labelKids\.push\(el\('span', \{class: 'ui-form-required'\}, \['required'\]\)\);/.test(src));
check('placeholders are italic and fainter than values', /input::placeholder, textarea::placeholder \{ color: var\(--text-mute\); opacity: 0\.8; font-style: italic; \}/.test(css));

// A row missing a required cell waits: not saved, and it says so.
var wcols = [{field: 'action', label: 'Action', required: true}, {field: 'limit', label: 'Times', required: true, type: 'number'}, {field: 'note', label: 'Note'}];
var cols = wcols;
function cellHidden() { return false; }
eval(liftBody(src, 'function cellFilled(v) {'));
eval(liftBody(src, 'function rowMissing(r) {'));
check('a row with both required cells is complete', rowMissing({action: 'send_email', limit: 3}).length === 0);
check('a row missing its number waits on it', rowMissing({action: 'send_email', limit: ''}).join() === 'Times');
check('an untouched row misses both', rowMissing({}).length === 2);
check('a zero someone typed counts as filled', rowMissing({action: 'a', limit: 0}).length === 0);
check('only complete rows are saved', /return cols\.some\(function\(c\) \{ return cellFilled\(r\[c\.field\]\); \}\) && rowMissing\(r\)\.length === 0;/.test(src));
check('an emptied number box is empty, not 0', /\(ctl\.value === '' \? '' : Number\(ctl\.value\)\)/.test(src));
check('a waiting row says what it waits on', /'Not saved until ' \+ gone\.join\(' and '\)/.test(src));

console.log(fail === 0 ? '\nALL ROWS CASES TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
