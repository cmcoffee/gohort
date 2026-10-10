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
  check('the host prelude goes in after the relay and before the page',
    /ISOLATE_SHIM \+ \(cfg\.isolate_prelude \|\| ''\) \+ \(html/.test(misc));
  check('audio and video sources are relayed like images',
    /audio\[src\],video\[src\],source\[src\]/.test(misc) && /parentNode\.load\(\)/.test(misc));
  return codePaths();
}).then(function() {
  if (fail) process.exit(1);
});

// Images, sounds and requests made in code, as a game library makes them,
// in a stand-in browser: a src accessor on the element prototypes, a native
// XMLHttpRequest, and the page's base URL the frame inherits.
function codePaths() {
  var listeners = [];
  function srcProp(C) {
    Object.defineProperty(C.prototype, 'src', {configurable: true, enumerable: true,
      get: function() { return this._src || ''; }, set: function(v) { this._src = String(v); }});
  }
  class HTMLImageElement extends EventTarget {}
  class HTMLMediaElement extends EventTarget {}
  class Audio extends HTMLMediaElement {}
  srcProp(HTMLImageElement); srcProp(HTMLMediaElement);
  // A real XMLHttpRequest calls its on<event> handlers for any event it
  // dispatches, a synthetic one included; a bare EventTarget does not.
  class NativeXHR extends EventTarget {
    constructor() {
      super(); this.responseType = '';
      var self = this;
      ['load', 'error', 'loadend', 'readystatechange'].forEach(function(t) {
        self.addEventListener(t, function(e) { if (typeof self['on' + t] === 'function') self['on' + t](e); });
      });
    }
    open(m, u) { this.nativeOpen = u; }
    send() { this.nativeSent = true; }
  }
  class ProgressEvent extends Event {}
  var posted = [];
  var w = {
    addEventListener: function(t, fn) { if (t === 'message') listeners.push(fn); },
    parent: {postMessage: function(m) { posted.push(m); }},
    Blob: Blob, FormData: FormData, ArrayBuffer: ArrayBuffer, Promise: Promise, Response: Response,
    String: String, Object: Object, Math: Math, Error: Error, URL: URL, JSON: JSON,
    Event: Event, ProgressEvent: ProgressEvent, TextDecoder: TextDecoder,
    HTMLImageElement: HTMLImageElement, Image: HTMLImageElement, HTMLMediaElement: HTMLMediaElement, Audio: Audio,
    XMLHttpRequest: NativeXHR,
    document: {documentElement: {}, baseURI: 'https://oddjob.example/apps/game/'},
  };
  w.window = w;
  var ctx = vm.createContext(w);
  scripts(iso).forEach(function(code) { vm.runInContext(code, ctx); });
  function last() { return posted[posted.length - 1]; }
  function answer(m, body, type, status) {
    listeners.forEach(function(fn) { fn({data: {__uiIsoReply: 1, id: m.id, status: status || 200, type: type, body: body}}); });
  }
  function refuse(m) { listeners.forEach(function(fn) { fn({data: {__uiIsoReply: 1, id: m.id, error: 'not allowed'}}); }); }
  function tick() { return new Promise(function(r) { setTimeout(r, 5); }); }
  var bytes = new Uint8Array([0x67, 0x6c, 0x54, 0x46]).buffer;

  // A loader hands fetch a Request, already absolute against the page.
  var n = posted.length;
  w.fetch(new Request('https://oddjob.example/apps/game/assets/ship.glb'));
  check('a Request for the app\'s own file is relayed as its relative path', posted.length === n + 1 && last().url === 'assets/ship.glb');
  var outside = 0;
  w.fetch('https://oddjob.example/apps/other/assets/x.glb').catch(function() { outside++; });
  w.fetch('https://cdn.example/model.glb').catch(function() { outside++; });

  var img = new w.Image();
  img.src = 'assets/brick.png';
  var imgMsg = last();
  check('an Image made in code relays its relative src', imgMsg.url === 'assets/brick.png' && img.src === '');
  answer(imgMsg, new Uint8Array([1]).buffer, 'image/png');

  var snd = new w.Audio('assets/hit.wav');
  var sndMsg = last();
  check('new Audio(path) relays its path', sndMsg.url === 'assets/hit.wav' && snd instanceof w.HTMLMediaElement);
  answer(sndMsg, new Uint8Array([1]).buffer, 'audio/wav');

  var inline = new w.Image();
  var before = posted.length;
  inline.src = 'data:image/png;base64,AA==';
  check('a data: src is set directly, not relayed', posted.length === before && inline.src.indexOf('data:') === 0);

  var x = new w.XMLHttpRequest(), loaded = 0, ended = 0;
  x.responseType = 'arraybuffer';
  x.onload = function() { loaded++; };
  x.addEventListener('loadend', function() { ended++; });
  x.open('GET', 'assets/level.bin');
  x.setRequestHeader('Accept', '*/*');
  x.send();
  var xMsg = last();
  check('a relative XMLHttpRequest goes through the relay', xMsg.url === 'assets/level.bin' && !x.nativeOpen);
  answer(xMsg, bytes, 'application/octet-stream');

  var j = new w.XMLHttpRequest();
  j.responseType = 'json';
  j.open('GET', 'data/level');
  j.send();
  answer(last(), new TextEncoder().encode('{"tiles":3}').buffer, 'application/json');

  var t = new w.XMLHttpRequest();
  t.open('GET', 'shared/scores');
  t.send();
  answer(last(), new TextEncoder().encode('[1,2]').buffer, 'application/json');

  var bad = new w.XMLHttpRequest(), errored = 0;
  bad.onerror = function() { errored++; };
  bad.open('GET', '/apps/other/data/x');
  bad.send();
  refuse(last());

  var native = new w.XMLHttpRequest();
  native.open('GET', 'https://cdn.example/lib.json');
  native.send();

  return tick().then(function() {
    check('an app\'s own absolute URL elsewhere, and another site, are refused', outside === 2);
    check('the Image gets the relayed bytes as a blob: URL', img.src.indexOf('blob:') === 0);
    check('the Audio too', snd.src.indexOf('blob:') === 0);
    check('an arraybuffer XHR finishes with status, bytes and its events',
      x.readyState === 4 && x.status === 200 && x.response instanceof ArrayBuffer && x.response.byteLength === 4 && loaded === 1 && ended === 1);
    check('the response type header reads back', x.getResponseHeader('content-type') === 'application/octet-stream');
    check('a json XHR parses', j.response && j.response.tiles === 3);
    check('a text XHR has responseText', t.responseText === '[1,2]' && t.response === '[1,2]');
    check('a refused XHR fires error with status 0', errored === 1 && bad.status === 0 && bad.readyState === 4);
    check('an absolute XHR is left to the browser', native.nativeOpen === 'https://cdn.example/lib.json' && native.nativeSent === true);
  });
}
