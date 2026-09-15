// DisplayPair is ONE type rendered by TWO components. record_view implemented
// items + block and ignored status_field; display_panel implemented
// status_field and ignored items + block — so an array pair written against a
// display_panel came out as the string "[object Object]". Both render through
// uiDisplayPair now; this is the harness that says so.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');

var fails = 0;
function ok(cond, msg) { if (!cond) { console.log('FAIL: ' + msg); fails++; } }

function mkNode(cls) {
  var n = {
    className: cls || '', style: {}, textContent: '', _attrs: {}, _kids: [],
    classList: { _s: (cls || '').split(' ').filter(Boolean),
      contains: function(c) { return this._s.indexOf(c) >= 0; } },
    setAttribute: function(k, v) { this._attrs[k] = v; },
    appendChild: function(k) { this._kids.push(k); return k; },
  };
  return n;
}
// el(tag, attrs, kids) — the prelude's builder, reduced to what pairs use.
function el(tag, attrs, kids) {
  var n = mkNode((attrs && attrs.class) || '');
  n.tag = tag;
  (kids || []).forEach(function(k) {
    if (typeof k === 'string') { n.textContent += k; return; }
    n.appendChild(k);
  });
  return n;
}
function fmt(v) { return v == null ? '' : String(v); }

// Pull uiDisplayPair (and its lookup dependency) out of the fragment.
var sandbox = { el: el, fmt: fmt };
var body = src.slice(src.indexOf('function lookup(obj, path)'));
body = body.slice(0, body.indexOf('function showToast('));
new Function('el', 'fmt', body + '; this.lookup = lookup; this.uiDisplayPair = uiDisplayPair;').call(sandbox, el, fmt);
var uiDisplayPair = sandbox.uiDisplayPair;

// Flatten a rendered node to the text a reader would see.
function textOf(n) {
  var out = n.textContent || '';
  (n._kids || []).forEach(function(k) { out += ' ' + textOf(k); });
  return out;
}

// 1. An array of objects renders its sub-pairs, not "[object Object]".
var wrap = mkNode();
uiDisplayPair(wrap, {
  actions: [
    { name: 'report', command: '/opt/bin/cap report', runs_in: 'folder' },
    { name: 'disk', command: '/opt/bin/cap disk', runs_in: 'folder' },
  ],
}, { label: 'Actions', field: 'actions', items: [
  { field: 'name', mono: true },
  { label: 'runs', field: 'command', block: true },
  { label: 'in', field: 'runs_in' },
]});
var txt = textOf(wrap).replace(/\s+/g, ' ');
ok(txt.indexOf('[object Object]') < 0, 'array pair must not stringify objects: ' + txt);
ok(txt.indexOf('report') >= 0, 'sub-pair value missing: ' + txt);
ok(txt.indexOf('/opt/bin/cap report') >= 0, 'block sub-pair missing: ' + txt);
ok(txt.indexOf('in: folder') >= 0, 'labelled sub-pair missing: ' + txt);

// 2. An empty array says so rather than rendering nothing.
var wrap2 = mkNode();
uiDisplayPair(wrap2, { actions: [] }, { label: 'Actions', field: 'actions', items: [{ field: 'name' }] });
ok(textOf(wrap2).indexOf('—') >= 0, 'empty list should show a dash');

// 3. status_field still colours a plain pair — the half display_panel had and
//    record_view did not.
var wrap3 = mkNode();
uiDisplayPair(wrap3, { state: 'Live', state_status: 'ok' },
  { label: 'State', field: 'state', status_field: 'state_status' });
var valNode = wrap3._kids[0]._kids[1];
ok(valNode.classList.contains('ok'), 'status_field should colour the value: ' + valNode.className);

// 4. A bad status colours too, and an unknown one renders plain.
var wrap4 = mkNode();
uiDisplayPair(wrap4, { s: 'x', st: 'nonsense' }, { label: 'S', field: 's', status_field: 'st' });
ok(!wrap4._kids[0]._kids[1].classList.contains('nonsense'), 'unknown severity must not become a class');

// 5. A block pair renders as its own row, and dotted paths resolve.
var wrap5 = mkNode();
uiDisplayPair(wrap5, { tool: { body: 'line one\nline two' } }, { label: 'Body', field: 'tool.body', block: true });
ok(wrap5._kids[0].classList.contains('ui-display-row-block'), 'block pair needs its own row');
ok(textOf(wrap5).indexOf('line two') >= 0, 'dotted path did not resolve: ' + textOf(wrap5));

console.log(fails === 0 ? 'PASS' : (fails + ' failure(s)'));
