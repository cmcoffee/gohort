// A nav entry with an icon (NavLink.Icon): not a tab but a control just right
// of the page's title, its glyph drawn and its label the tooltip. An entry
// naming a glyph the runtime does not draw keeps its label written instead.

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
    setAttribute: function (k, v) { this._attrs[k] = String(v); },
    getAttribute: function (k) { return this._attrs[k]; },
    appendChild: function (k) { this._kids.push(k); k.parentNode = n; return k; },
    insertBefore: function (k) { return this.appendChild(k); },
    addEventListener: function (e, f) { (this._on[e] = this._on[e] || []).push(f); },
    contains: function () { return false; },
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
var doc = {createElementNS: function (ns, tag) { return mkNode(tag); }, addEventListener: function () {}};
var win = {
  addEventListener: function () {},
  ResizeObserver: function () { this.observe = function () {}; },
  uiAnchorMenu: function () { return {isOpen: function () { return false; }, toggle: function () {}, close: function () {}}; },
};

var from = src.indexOf('  var uiNavGlyphs = {');
var to = src.indexOf('  function renderPageBody(cfg, root) {');
if (from < 0 || to < from) { console.log('FAIL navIcons or navStrip moved in 99_epilogue.js'); process.exit(1); }
var fns = new Function('el', 'document', 'window', src.slice(from, to) + '\nreturn {navIcons: navIcons, navStrip: navStrip};')(el, doc, win);

var nav = [
  {label: 'Records', url: 'records'},
  {label: 'Settings...', url: '_settings', icon: 'settings'},
  {label: 'Odd', url: 'odd', icon: 'no-such-glyph'},
];

var box = fns.navIcons({nav: nav});
check('the icon entries are drawn in a box of their own', box && box.className === 'ui-page-icons' && box._kids.length === 2);
var gear = box._kids[0];
check('an icon entry is a link to its page', gear.tagName === 'a' && gear.getAttribute('href') === '_settings' && gear.className === 'ui-page-icon');
check('its glyph is drawn', gear._kids[0].tagName === 'svg' && gear._kids[0]._kids.length === 1 && gear._kids[0]._kids[0].tagName === 'path');
check('the glyph is shown alone, its label not written', gear._kids.length === 1);
check('its label names it for a reader that cannot see the glyph', gear.getAttribute('title') === 'Settings...' && gear.getAttribute('aria-label') === 'Settings...');
check('a glyph the runtime does not draw leaves the label alone', box._kids[1]._kids.length === 1 && text(box._kids[1]) === 'Odd');
check('a page with no icon entries has no box', fns.navIcons({nav: [nav[0]]}) === null && fns.navIcons({}) === null);

var row = fns.navStrip({nav: nav});
var tabs = row._kids.filter(function (k) { return k.className.indexOf('ui-page-tab-more') < 0; });
check('an icon entry is not a tab', tabs.length === 1 && text(tabs[0]) === 'Records');
check('a page whose only entries are icons has no tab row', fns.navStrip({nav: [nav[1]]}) === null);

// Where the header puts them: just right of the page's title, inside its
// left group, so they are the page's, not beside the live pill and the bell.
var titleAt = src.indexOf("headerLeft.appendChild(el('h1'");
var iconsAt = src.indexOf('if (icons) headerLeft.appendChild(icons);');
var leftAt = src.indexOf('header.appendChild(headerLeft);');
check('the icons follow the title, in its group', titleAt > 0 && iconsAt > titleAt && leftAt > iconsAt);
check('they are added once, there', src.split('appendChild(icons)').length === 2);

if (fail) { console.log(fail + ' failed'); process.exit(1); }
