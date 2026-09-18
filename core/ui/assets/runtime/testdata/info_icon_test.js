// What uiInfoIcon actually BUILDS, not what the source says it builds.
//
// The bug this pins shipped: the button carried a native `title` as well as
// opening the styled popover, so hovering an icon produced two tooltips, the
// panel at 120ms and the browser's own a beat later, drawn over each other.
// A grep of the source would have missed it once the attribute moved, so the
// emitted DOM is what gets checked here.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

function mkNode(tag) {
  var n = {
    tagName: tag, className: '', style: {}, textContent: '', dataset: {},
    _attrs: {}, _kids: [], _on: {}, _removed: false,
    classList: {
      _s: [],
      add: function (c) { if (this._s.indexOf(c) < 0) this._s.push(c); },
      remove: function (c) { var i = this._s.indexOf(c); if (i >= 0) this._s.splice(i, 1); },
      contains: function (c) { return this._s.indexOf(c) >= 0; },
    },
    setAttribute: function (k, v) { this._attrs[k] = v; },
    getAttribute: function (k) { return this._attrs[k]; },
    removeAttribute: function (k) { delete this._attrs[k]; },
    hasAttribute: function (k) { return this._attrs[k] !== undefined; },
    appendChild: function (k) { this._kids.push(k); return k; },
    removeChild: function (k) { var i = this._kids.indexOf(k); if (i >= 0) this._kids.splice(i, 1); },
    remove: function () { this._removed = true; },
    addEventListener: function (e, f) { (this._on[e] = this._on[e] || []).push(f); },
    removeEventListener: function () {},
    contains: function () { return false; },
    focus: function () {},
    getBoundingClientRect: function () { return {left: 10, top: 10, right: 26, bottom: 26, width: 16, height: 16}; },
    querySelector: function () { return null; },
    offsetWidth: 300, offsetHeight: 120,
  };
  return n;
}

// The prelude is one IIFE, so the function under test is lifted out of it the
// way the other harnesses here do, with its free variables injected.
function el(tag, opts, kids) {
  var n = mkNode(tag);
  if (opts) {
    for (var k in opts) {
      if (k === 'class') n.className = opts[k];
      else if (k === 'text') n.textContent = opts[k];
      else if (k.indexOf('on') === 0) n.addEventListener(k.slice(2), opts[k]);
      else n.setAttribute(k, opts[k]);
    }
  }
  (kids || []).forEach(function (c) { if (c != null) n.appendChild(c); });
  return n;
}
var body = mkNode('body');
var documentStub = {
  body: body,
  createElement: mkNode,
  addEventListener: function () {},
  removeEventListener: function () {},
};
var windowStub = {
  innerWidth: 1200, innerHeight: 800,
  addEventListener: function () {}, removeEventListener: function () {},
};
// The prelude OPENS an IIFE that the epilogue closes, so the file cannot be
// wrapped whole; take the span that defines the icon and nothing else.
var from = src.indexOf('var infoSeq = 0;');
var to = src.indexOf('window.uiInfoIcon = uiInfoIcon;');
if (from < 0 || to < from) { console.log('FAIL uiInfoIcon moved in 00_prelude.js'); process.exit(1); }
var span = src.slice(from, to);
var lift = new Function('el', 'document', 'window', 'setTimeout', 'clearTimeout',
  span + '\nreturn uiInfoIcon;');
var uiInfoIcon = lift(el, documentStub, windowStub, function () { return 0; }, function () {});

check('no detail, no icon', uiInfoIcon('') === null);
check('blank detail, no icon', uiInfoIcon('   ') === null);

var btn = uiInfoIcon('The long half of the copy.\n\nA second paragraph.');
check('an icon is built', !!btn);
check('it is a button, not a link', btn.tagName === 'button');
check('it does not submit the form it sits in', btn.getAttribute('type') === 'button');
check('it carries the info glyph', btn._kids[0] === '\u24d8', JSON.stringify(btn._kids[0]));

// THE REGRESSION. A title here is a second tooltip on top of the popover.
check('no native title attribute', !btn.hasAttribute('title'),
  'title=' + JSON.stringify(btn.getAttribute('title')));

check('it is labelled for a screen reader', !!btn.getAttribute('aria-label'));
check('it starts closed', btn.getAttribute('aria-expanded') === 'false');
check('nothing is described before it opens', !btn.hasAttribute('aria-describedby'));

// Opening: click is the route that works on a touch device.
btn._on.click[0]({preventDefault: function () {}, stopPropagation: function () {}});
check('the panel is in the document', body._kids.length === 1);
check('it reads as open', btn.getAttribute('aria-expanded') === 'true');
check('the panel describes the button', !!btn.getAttribute('aria-describedby'));
check('the panel id matches what the button points at',
  body._kids[0].getAttribute('id') === btn.getAttribute('aria-describedby'));
check('the panel is a tooltip', body._kids[0].getAttribute('role') === 'tooltip');
check('a blank line stays a paragraph break', body._kids[0]._kids.length === 2,
  'paragraphs=' + body._kids[0]._kids.length);

// Two icons on one form must not claim the same id.
var other = uiInfoIcon('Another field.');
other._on.click[0]({preventDefault: function () {}, stopPropagation: function () {}});
check('a second icon gets its own id',
  other.getAttribute('aria-describedby') !== btn.getAttribute('aria-describedby'));

// Escape closes the popover and says so, rather than leaving a stale state.
btn._on.click[0]({preventDefault: function () {}, stopPropagation: function () {}});
check('clicking again closes it', btn.getAttribute('aria-expanded') === 'false');
check('and stops describing', !btn.hasAttribute('aria-describedby'));

process.exit(fail ? 1 : 0);
