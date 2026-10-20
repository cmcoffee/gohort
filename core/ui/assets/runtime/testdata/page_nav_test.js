// The page menu (navStrip): what the runtime BUILDS.
//
// It keeps the tabs that fit on the header row and folds the rest behind a
// More button, last first, never the page you are on. Widths are faked: each
// tab is ten pixels a letter, More is sixty, and the row is as wide as a test
// says.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

function mkNode(tag) {
  var n = {
    tagName: tag, className: '', style: {}, _attrs: {}, _kids: [], _on: {}, parentNode: null,
    classList: {
      add: function (c) { if (!this.contains(c)) n.className = (n.className + ' ' + c).trim(); },
      remove: function (c) { n.className = n.className.split(' ').filter(function (x) { return x && x !== c; }).join(' '); },
      contains: function (c) { return n.className.split(' ').indexOf(c) >= 0; },
      toggle: function (c, on) { if (on === undefined ? !this.contains(c) : on) this.add(c); else this.remove(c); },
    },
    setAttribute: function (k, v) { this._attrs[k] = String(v); },
    getAttribute: function (k) { return this._attrs[k]; },
    appendChild: function (k) { return this.insertBefore(k, null); },
    insertBefore: function (k, ref) {
      if (k.parentNode) k.parentNode._kids.splice(k.parentNode._kids.indexOf(k), 1);
      var i = ref ? this._kids.indexOf(ref) : -1;
      if (i < 0) this._kids.push(k); else this._kids.splice(i, 0, k);
      k.parentNode = n;
      return k;
    },
    get firstChild() { return this._kids[0] || null; },
    addEventListener: function (e, f) { (this._on[e] = this._on[e] || []).push(f); },
    contains: function (other) {
      for (var p = other; p; p = p.parentNode) if (p === n) return true;
      return false;
    },
    focus: function () {},
  };
  return n;
}
function el(tag, opts, kids) {
  var n = mkNode(tag);
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else n.setAttribute(k, opts[k]);
  }
  (kids || []).forEach(function (c) {
    if (c == null) return;
    n.appendChild(typeof c === 'string' ? {text: c, _kids: []} : c);
  });
  return n;
}
function text(n) {
  return (n._kids || []).map(function (k) { return k.text !== undefined ? k.text : text(k); }).join('');
}
function fire(target, ev, e) {
  (target._on[ev] || []).forEach(function (f) { f(e || {stopPropagation: function () {}}); });
}

// --- the page menu ------------------------------------------------------

var from = src.indexOf('  function navStrip(cfg) {');
var to = src.indexOf('  function renderPageBody(cfg, root) {');
if (from < 0 || to < from) { console.log('FAIL navStrip moved in 99_epilogue.js'); process.exit(1); }

function strip(nav) {
  var doc = mkNode('document');
  var observed = null;
  var menuOpen = false, menuEl = null;
  var win = {
    addEventListener: function () {},
    ResizeObserver: function (cb) { observed = cb; this.observe = function () {}; },
    uiAnchorMenu: function (toggle, menu) {
      menuEl = menu;
      return {
        isOpen: function () { return menuOpen; },
        open: function () { menuOpen = true; },
        close: function () { menuOpen = false; },
        toggle: function () { menuOpen = !menuOpen; },
      };
    },
  };
  var navStrip = new Function('el', 'document', 'window', src.slice(from, to) + '\nreturn navStrip;')(el, doc, win);
  var row = navStrip({nav: nav});
  if (!row) return null;
  // The row's own width is set by a test; what is in it is measured.
  var more = row._kids[row._kids.length - 1];
  var menu = null;
  Object.defineProperty(row, 'scrollWidth', {get: function () {
    return row._kids.reduce(function (w, k) {
      if (k.style.display === 'none') return w;
      return w + (k === more ? 60 : text(k).length * 10);
    }, 0);
  }});
  row.clientWidth = 0;
  return {
    row: row, more: more, doc: doc,
    resize: function (w) { row.clientWidth = w; observed(); },
    shown: function () { return row._kids.filter(function (k) { return k !== more; }).map(text); },
    folded: function () { return menuEl._kids.map(text); },
    menuOpen: function () { return menuOpen; },
  };
}

check('no nav, no row', strip([]) === null);

var nav = ['System', 'Activity', 'Costs', 'LLMs', 'Capabilities', 'Agents'].map(function (l, i) {
  return {label: l, url: '/' + l.toLowerCase(), active: l === 'Activity'};
});
var h = strip(nav);
h.resize(0);
check('before it has a width, nothing moves', h.shown().length === 6 && h.more.style.display === 'none');
h.resize(1000);
check('room for all: every tab on the row, no More', h.shown().join() === 'System,Activity,Costs,LLMs,Capabilities,Agents' && h.more.style.display === 'none');
h.resize(250);
check('too narrow: More shows', h.more.style.display === '');
check('the last tabs fold first, the rest keep their order', h.shown().join() === 'System,Activity,Costs', h.shown().join());
check('what is left fits beside More', h.row.scrollWidth <= 250, h.row.scrollWidth);
check('the folded tabs are in the menu, in order', h.folded().join() === 'LLMs,Capabilities,Agents');
h.resize(160);
check('the page you are on stays on the row', h.shown().indexOf('Activity') >= 0, h.shown().join());
h.resize(1000);
check('widening puts every tab back', h.shown().length === 6 && h.folded().length === 0 && h.more.style.display === 'none');

var last = nav.map(function (it) { return {label: it.label, url: it.url, active: it.label === 'Agents'}; });
var l = strip(last);
l.resize(200);
check('a current tab at the end stays, and the ones before it fold', l.shown().indexOf('Agents') >= 0 && l.row.scrollWidth <= 200, l.shown().join());
check('the row keeps its order around it', l.shown().join() === 'System,Agents', l.shown().join());

// The More menu opens from its button and closes the ways a reader expects.
h.resize(250);
fire(h.more, 'click');
check('More opens the menu', h.menuOpen());
fire(h.doc, 'click', {target: mkNode('div')});
check('a click outside closes it', !h.menuOpen());
fire(h.more, 'click');
fire(h.doc, 'keydown', {key: 'Escape'});
check('Escape closes it', !h.menuOpen());
fire(h.more, 'click');
h.resize(300);
check('a resize closes it', !h.menuOpen());

if (fail) process.exit(1);
