// FormField.SuggestionsSource: a text field's button asks for choices built
// from the form as it stands, then offers them in a filterable list. Run
// here against stand-ins for the DOM and fetch: the request carries the form's
// unsaved values, the choices fill the typing suggestions, a pick fills and
// saves the field, a refusal is shown in place of choices, and a second click
// while waiting stops the wait.
var fs = require('fs');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// --- stand-ins -------------------------------------------------------------
function fakeEl(tag, attrs, kids) {
  var n = {
    tag: tag, attrs: attrs || {}, children: [], listeners: {}, style: {}, value: '', title: '',
    disabled: false, id: '', cls: {},
    classList: {add: function(c) { n.cls[c] = true; }, remove: function(c) { delete n.cls[c]; }},
    appendChild: function(c) { n.children.push(c); c.parent = n; return c; },
    setAttribute: function(k, v) { n.attrs[k] = v; },
    getAttribute: function(k) { return n.attrs[k]; },
    addEventListener: function(ev, fn) { (n.listeners[ev] = n.listeners[ev] || []).push(fn); },
    fire: function(ev, arg) { (n.listeners[ev] || []).forEach(function(fn) { fn(arg || {preventDefault: function() {}}); }); },
  };
  Object.defineProperty(n, 'childNodes', {get: function() { return n.children; }});
  Object.defineProperty(n, 'innerHTML', {set: function() { n.children = []; }, get: function() { return ''; }});
  Object.defineProperty(n, 'textContent', {
    get: function() { return n.children.map(function(c) { return typeof c === 'string' ? c : (c.text != null ? c.text : c.textContent); }).join(''); },
    set: function(v) { n.children = v ? [{text: String(v)}] : []; },
  });
  if (attrs && attrs.type) n.type = attrs.type;
  (kids || []).forEach(function(k) { n.children.push(typeof k === 'string' ? {text: k} : k); });
  return n;
}
var el = fakeEl;
function find(node, pred, out) {
  out = out || [];
  if (node && typeof node === 'object') {
    if (pred(node)) out.push(node);
    (node.children || []).forEach(function(c) { find(c, pred, out); });
  }
  return out;
}
var window = {};
var modals = [];
window.uiOpenModal = function(opts) {
  var m = {opts: opts, body: fakeEl('div'), closed: false};
  m.close = function() { m.closed = true; };
  opts.mount(m.body, m);
  modals.push(m);
  return m;
};
var fetchCalls = [];
var nextFetch = null;
function fetch(url, init) {
  fetchCalls.push({url: url, init: init});
  return nextFetch(init);
}
function okJSON(v) { return Promise.resolve({ok: true, json: function() { return Promise.resolve(v); }}); }
function AbortController() {
  var c = this;
  c.signal = {aborted: false};
  c.abort = function() { c.signal.aborted = true; if (c.signal.onabort) c.signal.onabort(); };
}
var saved = [];
function save(field, value) { saved.push([field, value]); }
var current = {provider: 'vllm', endpoint: 'http://unsaved:8000/v1'};

// --- the code under test, as shipped ----------------------------------------
var busySrc = prelude.match(/  function busyButton\(btn, stoppable\) \{[\s\S]*?\n  \}\n/);
var fnSrc = basics.match(/    function attachSuggestionsSource\(f, input, dl, host\) \{[\s\S]*?\n    \}\n/);
check('busyButton found', !!busySrc);
check('attachSuggestionsSource found', !!fnSrc);
eval(busySrc[0]);
eval(fnSrc[0]);
check('the text field calls it when a source is declared',
  /if \(f\.suggestions_source\) attachSuggestionsSource\(f, input, dl, fieldWrap\);/.test(basics));

var f = {field: 'model', label: 'Model', suggestions_source: 'api/x/models', suggestions_label: 'List models'};
var host = fakeEl('div'), input = fakeEl('input'), dl = fakeEl('datalist');
input.value = 'typed-but-unsaved';
attachSuggestionsSource(f, input, dl, host);
var btn = find(host, function(n) { return n.tag === 'button'; })[0];
check('the button carries the label', btn && btn.textContent === 'List models');

function settle() { return new Promise(function(r) { setTimeout(r, 20); }); }

nextFetch = function() {
  return okJSON({suggestions: ['b-model', 'a-model', {value: 'c-model', label: 'C model', help: 'the third'}], note: 'What vllm offers.'});
};
btn.fire('click');
check('it spins while waiting', /^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]/.test(btn.textContent));
settle().then(function() {
  var body = JSON.parse(fetchCalls[0].init.body);
  check('it POSTs to the source', fetchCalls[0].url === 'api/x/models' && fetchCalls[0].init.method === 'POST');
  check('the form\'s unsaved values ride along', body.endpoint === 'http://unsaved:8000/v1' && body.provider === 'vllm');
  check('the field\'s own typed value rides along', body.model === 'typed-but-unsaved');
  check('the label comes back once done', btn.textContent === 'List models');
  check('the choices fill the typing suggestions', dl.children.length === 3 && dl.children[2].value === 'c-model');
  var m = modals[0];
  check('a picker opens with the note', m && m.opts.subtitle === 'What vllm offers.');
  var items = find(m.body, function(n) { return n.cls === undefined ? false : n.attrs.class === 'ui-suggest-item'; });
  check('every choice is listed', items.length === 3);
  check('a labelled choice shows its label and help',
    /C model/.test(JSON.stringify(items[2].children.map(function(c) { return c.textContent; }))) &&
    find(items[2], function(n) { return n.attrs && n.attrs.class === 'ui-suggest-item-hook'; }).length === 1);
  var filter = find(m.body, function(n) { return n.tag === 'input'; })[0];
  filter.value = 'c-mo';
  filter.fire('input');
  items = find(m.body, function(n) { return n.attrs && n.attrs.class === 'ui-suggest-item'; });
  check('the filter narrows the list', items.length === 1);
  filter.fire('keydown', {key: 'Enter', preventDefault: function() {}});
  check('Enter picks the first match and fills the field', input.value === 'c-model');
  check('the pick is saved', saved.length === 1 && saved[0][0] === 'model' && saved[0][1] === 'c-model');
  check('picking closes the picker', m.closed);

  // A refusal is shown in place of choices.
  nextFetch = function() { return okJSON({error: 'Could not list the models: invalid api key'}); };
  btn.fire('click');
  return settle();
}).then(function() {
  var m = modals[1];
  check('a refusal opens the picker with the reason',
    m && /invalid api key/.test(m.body.textContent) && find(m.body, function(n) { return n.tag === 'input'; }).length === 0);

  // A server error status is a reason too.
  nextFetch = function() {
    return Promise.resolve({ok: false, status: 502, text: function() { return Promise.resolve('upstream down'); }});
  };
  btn.fire('click');
  return settle();
}).then(function() {
  check('an HTTP failure is shown as its text', modals[2] && /upstream down/.test(modals[2].body.textContent));

  // A second click while waiting stops the wait, and no picker opens.
  nextFetch = function(init) {
    return new Promise(function(resolve, reject) {
      init.signal.onabort = function() { var e = new Error('aborted'); e.name = 'AbortError'; reject(e); };
    });
  };
  btn.fire('click');
  btn.fire('click');
  return settle();
}).then(function() {
  check('a second click stops the wait without a picker', modals.length === 3 && btn.textContent === 'List models');
  btn.fire('click');
  check('and the button works again after', fetchCalls.length === 5);
  if (fail) { console.log(fail + ' failed'); process.exit(1); }
  console.log('all passed');
  process.exit(0);
}).catch(function(e) { console.log('FAIL threw: ' + (e && e.stack || e)); process.exit(1); });
