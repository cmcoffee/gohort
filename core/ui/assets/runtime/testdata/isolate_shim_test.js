// An app page's isolated frame: the relay that carries its fetches to the
// parent page, and the storage it gets in an opaque origin.
//
// Runs the real shim strings from the runtime source in a stand-in window.
var fs = require('fs');
var vm = require('vm');
var dir = __dirname + '/..';

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// The value of a `var NAME = '...' + ...;` string constant in a runtime file.
function constant(src, name) {
  var i = src.indexOf('var ' + name + ' = ');
  if (i < 0) { console.log('FAIL ' + name + ' is gone'); process.exit(1); }
  var j = src.indexOf("';\n", i);
  return vm.runInNewContext(src.slice(i + ('var ' + name + ' = ').length, j + 1));
}
function scripts(html) {
  return html.split('<script>').slice(1).map(function(s) { return s.split('</' + 'script>')[0]; });
}

var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
var misc = fs.readFileSync(dir + '/70_misc.js', 'utf8');
var storage = constant(prelude, 'STORAGE_SHIM');
var iso = constant(misc, 'ISOLATE_SHIM');

// A window whose storage throws, as an opaque origin's does.
function frameWindow() {
  var posted = [];
  var w = {
    posted: posted,
    addEventListener: function() {},
    parent: {postMessage: function(m) { posted.push(m); }},
    Blob: Blob, FormData: FormData, ArrayBuffer: ArrayBuffer, Promise: Promise, Response: function() {},
    String: String, Object: Object, Math: Math, Error: Error, URL: URL,
    document: {documentElement: {}},
  };
  ['localStorage', 'sessionStorage'].forEach(function(n) {
    Object.defineProperty(w, n, {configurable: true, get: function() { throw new Error('SecurityError'); }});
  });
  w.window = w;
  return w;
}

var w = frameWindow();
var ctx = vm.createContext(w);
scripts(storage + iso).forEach(function(code) { vm.runInContext(code, ctx); });

var ls;
try { ls = w.localStorage; ls.setItem('best', 42); } catch (e) { ls = null; }
check('localStorage works in an app frame instead of throwing on first touch', ls && ls.getItem('best') === '42');
check('sessionStorage too', (function() { try { w.sessionStorage.setItem('a', 'b'); return w.sessionStorage.getItem('a') === 'b'; } catch (e) { return false; } })());

w.fetch('records', {method: 'POST', body: '{"score":3}', headers: {'Content-Type': 'application/json'}});
var blob = new Blob(['PNGDATA'], {type: 'image/png'});
w.fetch('assets/hero.png', {method: 'PUT', body: blob});
var buf = new Uint8Array([1, 2, 3]).buffer;
w.fetch('action/save', {method: 'POST', body: buf});
var fd = new FormData();
fd.append('name', 'level1');
fd.append('file', new Blob(['x'], {type: 'text/plain'}), 'map.txt');
w.fetch('action/upload', {method: 'POST', body: fd});

var m = w.posted;
check('a string body crosses as the string', m[0] && m[0].body === '{"score":3}' && m[0].ctype === 'application/json');
check('a Blob crosses as the Blob, not "[object Blob]"', m[1] && m[1].body instanceof Blob && m[1].body.type === 'image/png');
check('an ArrayBuffer crosses as bytes', m[2] && m[2].body instanceof ArrayBuffer && m[2].body.byteLength === 3);
check('FormData crosses as its entries, files included',
  m[3] && m[3].body && Array.isArray(m[3].body.__uiForm) && m[3].body.__uiForm.length === 2 &&
  m[3].body.__uiForm[0][0] === 'name' && m[3].body.__uiForm[1][1] instanceof Blob);
var refused = false;
w.fetch('https://example.com/x').catch(function() { refused = true; }).then(function() {
  check('an absolute URL is still refused', refused);
  // The parent rebuilds FormData and leaves its Content-Type to the browser.
  check('the parent rebuilds FormData and sets no Content-Type for it',
    /if \(d\.body\.__uiForm\) \{[\s\S]*?new FormData\(\)[\s\S]*?opts\.body = fd;\s*\}/.test(misc));
  check('the frame gets the storage polyfill ahead of the relay',
    /f\.setAttribute\('srcdoc', STORAGE_SHIM \+ ISOLATE_SHIM \+/.test(misc));
  check('audio and video sources are relayed like images',
    /audio\[src\],video\[src\],source\[src\]/.test(misc) && /parentNode\.load\(\)/.test(misc));
  if (fail) process.exit(1);
});
