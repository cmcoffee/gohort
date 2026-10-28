// The conversation's widescreen switch: off by default, remembered per device,
// shown only while the pane is wide enough for it to change anything, and the
// width itself the stylesheet's (.ui-agent-wide).
var fs = require('fs');
var dir = __dirname + '/..';
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(src, start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}
var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

function classes() {
  var set = {};
  return {toggle: function(c, on) { if (on) set[c] = true; else delete set[c]; }, has: function(c) { return !!set[c]; }};
}
function el(tag, attrs, kids) {
  var n = {tag: tag, attrs: {}, style: {}, textContent: (kids || []).join(''), on: {},
    setAttribute: function(k, v) { this.attrs[k] = v; },
    addEventListener: function(t, f) { this.on[t] = f; }};
  Object.keys(attrs || {}).forEach(function(k) { n.attrs[k] = attrs[k]; });
  return n;
}
var store = {}, broken = false, observed = [];
var window = {
  localStorage: {
    getItem: function(k) { if (broken) throw new Error('blocked'); return k in store ? store[k] : null; },
    setItem: function(k, v) { if (broken) throw new Error('blocked'); store[k] = v; },
  },
  ResizeObserver: function(fn) { this.observe = function(n) { observed.push({node: n, fn: fn}); }; },
};
eval(lift(panel, "var uiWideKey = ", 'uiWideKey').replace(/\s*$/, '') + ';');
eval('var uiWideMinPane = ' + /var uiWideMinPane = (\d+);/.exec(panel)[1] + ';');
eval('var uiWideGap = ' + /var uiWideGap = (\d+);/.exec(panel)[1] + ';');
eval(lift(panel, 'function uiWidescreenToggle(root, pane, log)', 'uiWidescreenToggle'));

function mount(width, bar) {
  var root = {classList: classes()}, kids = [];
  var pane = {clientWidth: width, appendChild: function(n) { kids.push(n); }};
  var log = {offsetWidth: width, clientWidth: width - (bar || 0)};
  var btn = uiWidescreenToggle(root, pane, log);
  return {root: root, pane: pane, log: log, btn: btn, kids: kids};
}

var p = mount(1400);
check('the switch is in the conversation pane', p.kids[0] === p.btn);
check('off by default', !p.root.classList.has('ui-agent-wide') && p.btn.attrs['aria-pressed'] === 'false');
check('and says what pressing it does', /Widescreen/.test(p.btn.title));
check('shown on a wide pane', p.btn.style.display === '');
p.btn.on.click();
check('pressing it widens the column', p.root.classList.has('ui-agent-wide') && p.btn.attrs['aria-pressed'] === 'true');
check('and this device remembers', store['ui.agent.widescreen'] === '1');
var again = mount(1400);
check('so the next page opens wide', again.root.classList.has('ui-agent-wide'));
again.btn.on.click();
check('pressing again goes back', !again.root.classList.has('ui-agent-wide') && store['ui.agent.widescreen'] === '0');

var narrow = mount(800);
check('a pane too narrow for it to matter hides it', narrow.btn.style.display === 'none');
var watch = observed.filter(function(o) { return o.node === narrow.pane; });
narrow.pane.clientWidth = 1200;
if (watch.length) watch[0].fn();
check('and shows it once the pane grows', watch.length === 1 && narrow.btn.style.display === '');

// Just inside the thread's scrollbar, however wide it is: none where it
// overlays, its width where it takes room.
check('where the scrollbar overlays it sits by the edge', mount(1400, 0).btn.style.right === uiWideGap + 'px');
check('beside a scrollbar that takes room', mount(1400, 15).btn.style.right === (15 + uiWideGap) + 'px');
var grows = mount(1400, 0), seen = observed.filter(function(o) { return o.node === grows.log; });
grows.log.clientWidth = 1400 - 15;
if (seen.length) seen[0].fn();
check('and moves in when the thread grows a scrollbar', seen.length === 1 && grows.btn.style.right === (15 + uiWideGap) + 'px');

broken = true;
var blocked = mount(1400);
blocked.btn.on.click();
check('with storage blocked it still switches', blocked.root.classList.has('ui-agent-wide'));
broken = false;

check('the panel puts it on its conversation pane', /convoPane\.appendChild\(convoLog\);\s*uiWidescreenToggle\(wrap, convoPane, convoLog\);/.test(panel));
var css = fs.readFileSync(dir + '/../runtime.css', 'utf8');
check('widescreen keeps a margin at each side', /\.ui-agent\.ui-agent-wide \{ --ui-agent-column: calc\(100% - \d+(\.\d+)?rem\); \}/.test(css));
process.exit(fail ? 1 : 0);
