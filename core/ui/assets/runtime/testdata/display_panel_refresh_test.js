// A DisplayPanel that refreshes keeps what it shows when one refresh fails
// (the server restarting under it), and paints again when the next one works;
// a first load that fails says so.
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
var basics = fs.readFileSync(dir + '/10_basics.js', 'utf8');

function node() {
  var n = {kids: [], _text: '', appendChild: function(c) { this.kids.push(c); }};
  Object.defineProperty(n, 'textContent', {
    get: function() { return this.kids.length ? this.kids.map(function(k) { return k.textContent; }).join('|') : this._text; },
    set: function(v) { this._text = v; this.kids = []; }});
  Object.defineProperty(n, 'innerHTML', {set: function() { this._text = ''; this.kids = []; }});
  return n;
}
function el() { return node(); }
function uiLoading() { var n = node(); n.textContent = 'Loading'; return n; }
function uiKeepBlockScroll() { return function() {}; }
function uiDisplayPair(wrap, data, p) { var n = node(); n.textContent = p.label + '=' + data[p.field]; wrap.appendChild(n); }
var tick = null;
function uiAutoRefresh(ms, fn) { tick = fn; }
var window = {addEventListener: function() {}};
var answers = [];
function fetchJSON() { return answers.shift()(); }
var components = {};
eval(lift(basics, 'components.display_panel = function(cfg)', 'display_panel'));
function settled() { return new Promise(function(r) { setTimeout(r, 10); }); }

(async function() {
  answers.push(function() { return Promise.resolve({running: '0.9.427'}); });
  var wrap = components.display_panel({source: 's', auto_refresh_ms: 5000, pairs: [{label: 'Running', field: 'running'}]});
  await settled();
  check('the first load paints', wrap.textContent === 'Running=0.9.427');

  answers.push(function() { return Promise.reject(new Error('Failed to fetch')); });
  tick();
  await settled();
  check('a refresh that fails keeps what was shown', wrap.textContent === 'Running=0.9.427');

  answers.push(function() { return Promise.resolve({running: '0.9.428'}); });
  tick();
  await settled();
  check('the next refresh paints again', wrap.textContent === 'Running=0.9.428');

  answers.push(function() { return Promise.reject(new Error('Failed to fetch')); });
  var first = components.display_panel({source: 's', pairs: [{label: 'Running', field: 'running'}]});
  await settled();
  check('a first load that fails says so', first.textContent === 'Failed: Failed to fetch');
  process.exit(fail ? 1 : 0);
})();
