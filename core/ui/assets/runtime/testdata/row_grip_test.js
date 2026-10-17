// What uiRowGrip does to the rows it moves (Table.ReorderURL): a drag puts the
// row where the pointer is among its own group's rows, the arrow keys step it
// one row, nothing moves when the table says not now, and the caller hears
// about a move only after the page shows it.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

// A DOM just big enough: a parent keeps its children in order, and each row
// is 40px tall, stacked in that order, so where it sits is what it reports.
function mkNode(tag, cls) {
  var n = {
    tagName: tag, parentNode: null, _kids: [], _on: {}, _attrs: {}, name: '',
    classList: {
      _s: (cls || '').split(' ').filter(Boolean),
      add: function (c) { if (this._s.indexOf(c) < 0) this._s.push(c); },
      remove: function (c) { var i = this._s.indexOf(c); if (i >= 0) this._s.splice(i, 1); },
      contains: function (c) { return this._s.indexOf(c) >= 0; },
    },
    setAttribute: function (k, v) { this._attrs[k] = v; },
    getAttribute: function (k) { return this._attrs[k]; },
    appendChild: function (k) { if (k.parentNode) k.parentNode._detach(k); k.parentNode = this; this._kids.push(k); return k; },
    insertBefore: function (k, ref) {
      if (k.parentNode) k.parentNode._detach(k);
      k.parentNode = this;
      var i = ref ? this._kids.indexOf(ref) : -1;
      if (i < 0) this._kids.push(k); else this._kids.splice(i, 0, k);
      return k;
    },
    _detach: function (k) { var i = this._kids.indexOf(k); if (i >= 0) this._kids.splice(i, 1); },
    addEventListener: function (e, f) { (this._on[e] = this._on[e] || []).push(f); },
    removeEventListener: function (e, f) { var l = this._on[e] || []; var i = l.indexOf(f); if (i >= 0) l.splice(i, 1); },
    fire: function (e, ev) { ev = ev || {}; ev.preventDefault = function () {}; (this._on[e] || []).slice().forEach(function (f) { f(ev); }); },
    focus: function () { focused = this; },
    setPointerCapture: function () {},
    getBoundingClientRect: function () {
      var i = this.parentNode._kids.indexOf(this);
      return {top: i * 40, height: 40, bottom: i * 40 + 40};
    },
  };
  Object.defineProperty(n, 'children', {get: function () { return n._kids; }});
  function sib(d) { return function () { var p = n.parentNode; if (!p) return null; return p._kids[p._kids.indexOf(n) + d] || null; }; }
  Object.defineProperty(n, 'previousElementSibling', {get: sib(-1)});
  Object.defineProperty(n, 'nextElementSibling', {get: sib(1)});
  Object.defineProperty(n, 'nextSibling', {get: sib(1)});
  return n;
}
var focused = null;
// After the press, a browser sends the pointer's moves and release to the
// document, never to the grip once its row has moved: moving a node drops a
// pointer capture. So the harness sends them there and nowhere else.
var doc = mkNode('document');
function el(tag, opts, kids) {
  var n = mkNode(tag, opts && opts['class']);
  for (var k in (opts || {})) if (k !== 'class') n.setAttribute(k, opts[k]);
  (kids || []).forEach(function (c) { if (typeof c === 'string') n.text = c; });
  return n;
}

var from = src.indexOf('  function uiRowGrip(');
var to = src.indexOf('  components.table = function(cfg) {');
if (from < 0 || to < from) { console.log('FAIL uiRowGrip moved in 10_basics.js'); process.exit(1); }
var uiRowGrip = new Function('el', 'document', src.slice(from, to) + '\nreturn uiRowGrip;')(el, doc);

// One group of four rows, with a heading-like non-row at the top that a row
// must never be moved past or counted as a place.
function setup(can) {
  var host = mkNode('div', 'band');
  host.appendChild(mkNode('div', 'heading'));
  var heard = [], busy = [], heardWhileBusy = [];
  ['a', 'b', 'c', 'd'].forEach(function (name) {
    var r = mkNode('div', 'ui-table-row');
    r.name = name;
    r.grip = uiRowGrip(r, 'ui-table-row', {
      can: function () { return can === undefined ? true : can; },
      moved: function (how) { heard.push(how + ':' + order(host)); heardWhileBusy.push(busy[busy.length - 1] === true); },
      busy: function (on) { busy.push(on); },
    });
    r.appendChild(r.grip);
    host.appendChild(r);
  });
  return {host: host, heard: heard, busy: busy, heardWhileBusy: heardWhileBusy, row: function (n) { return host._kids.filter(function (k) { return k.name === n; })[0]; }};
}
function order(host) {
  return host._kids.map(function (k) { return k.name || '|'; }).join('');
}

var g = setup();
var grip = g.row('a').grip;
check('the grip is a button that does not submit a form', grip.tagName === 'button' && grip.getAttribute('type') === 'button');
check('the grip says how to use it without a mouse', /arrow keys/.test(grip.getAttribute('aria-label')));

// Drag a down past c (rows sit at 40..200; c's middle is 140).
grip.fire('pointerdown', {button: 0, clientY: 60, pointerId: 1});
check('a drag says it has begun', g.busy.join() === 'true');
doc.fire('pointermove', {clientY: 150});
check('the row follows the pointer', order(g.host) === '|bcad', order(g.host));
check('nothing is saved mid-drag', g.heard.length === 0);
doc.fire('pointerup', {});
check('the drop is reported with the order the page shows', g.heard.join() === 'drop:|bcad', g.heard.join());
check('the move is reported before the drag is said to end', g.heardWhileBusy.join() === 'true' && g.busy.join() === 'true,false', g.heardWhileBusy.join());

// Several places in one drag, the case a captured grip lost: a then c then
// past d, all in one press.
g = setup();
grip = g.row('a').grip;
grip.fire('pointerdown', {button: 0, clientY: 60, pointerId: 1});
doc.fire('pointermove', {clientY: 110});
doc.fire('pointermove', {clientY: 150});
doc.fire('pointermove', {clientY: 190});
check('one drag carries the row several places', order(g.host) === '|bcda', order(g.host));
doc.fire('pointerup', {});
check('and the release is heard after it moved', g.heard.join() === 'drop:|bcda' && g.busy.join() === 'true,false', g.heard.join());

// A second finger's moves are not this drag's.
g = setup();
grip = g.row('a').grip;
grip.fire('pointerdown', {button: 0, clientY: 60, pointerId: 1});
doc.fire('pointermove', {clientY: 999, pointerId: 2});
check('another pointer does not move the row', order(g.host) === '|abcd', order(g.host));
doc.fire('pointerup', {pointerId: 2});
check('nor end the drag', g.busy.join() === 'true');
doc.fire('pointerup', {pointerId: 1});

// Past the end, then back to the top: never above the heading.
g = setup();
grip = g.row('b').grip;
grip.fire('pointerdown', {button: 0, clientY: 100, pointerId: 1});
doc.fire('pointermove', {clientY: 999});
check('past the last row it goes last', order(g.host) === '|acdb', order(g.host));
doc.fire('pointermove', {clientY: -50});
check('above the first row it goes first, still under the heading', order(g.host) === '|bacd', order(g.host));
doc.fire('pointerup', {});

// A wobble is a click, not a move.
g = setup();
grip = g.row('c').grip;
grip.fire('pointerdown', {button: 0, clientY: 140, pointerId: 1});
doc.fire('pointermove', {clientY: 142});
doc.fire('pointerup', {});
check('a pointer that hardly moved changes nothing', order(g.host) === '|abcd' && g.heard.length === 0, g.heard.join());

// The listeners go with the drag: a later stray move does nothing.
doc.fire('pointermove', {clientY: 999});
check('the drag leaves no listener on the document', (doc._on.pointermove || []).length === 0 && (doc._on.pointerup || []).length === 0);
check('a drag that ended stops listening', order(g.host) === '|abcd');

// Arrow keys step one row and keep the grip focused.
g = setup();
grip = g.row('b').grip;
grip.fire('keydown', {key: 'ArrowDown'});
check('down steps one row', order(g.host) === '|acbd', order(g.host));
check('the grip keeps focus so the next press moves it again', focused === grip);
grip.fire('keydown', {key: 'ArrowUp'});
grip.fire('keydown', {key: 'ArrowUp'});
check('up steps back, and stops at the first row', order(g.host) === '|bacd', order(g.host));
grip.fire('keydown', {key: 'ArrowUp'});
check('the heading is not a row to step past', order(g.host) === '|bacd', order(g.host));
check('each key move is reported', g.heard.join() === 'key:|acbd,key:|abcd,key:|bacd', g.heard.join());

// Not now: nothing moves and nothing is reported.
g = setup(false);
grip = g.row('a').grip;
grip.fire('keydown', {key: 'ArrowDown'});
grip.fire('pointerdown', {button: 0, clientY: 60, pointerId: 1});
doc.fire('pointermove', {clientY: 999});
doc.fire('pointerup', {});
check('a table that says not now moves nothing', order(g.host) === '|abcd' && g.heard.length === 0 && g.busy.length === 0);

// A right-click is not a drag.
g = setup();
g.row('a').grip.fire('pointerdown', {button: 2, clientY: 60, pointerId: 1});
check('only the main button drags', g.busy.length === 0);

// The table side: the wiring this harness cannot run.
check('the table posts every row key as {order: [...]}', /JSON\.stringify\(\{order: keys\}\)/.test(src));
check('the posted order comes from records, not the page', /var keys = records\.map\(/.test(src));
check('a reload waits for a drag, a save, or a queued save', /if \(dragging \|\| savingOrder \|\| orderTimer \|\| anyExpansionOpen\(\)\)/.test(src));
check('a failed save says so and puts back the server order', /The new order was not saved/.test(src));
check('no grips while the search box narrows the list', /cfg\.reorder_url && !searchText && rowKey/.test(src));
check('a fixed row keeps its key but gets no grip', /hasAttribute\('data-row-key'\) && !\(cfg\.reorder_fixed && lookup\(rec, cfg\.reorder_fixed\)\)/.test(src));

console.log(fail === 0 ? '\nALL ROW GRIP TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
