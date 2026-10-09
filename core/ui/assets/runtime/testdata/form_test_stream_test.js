// A FormPanel test that runs several checks streams a line as each starts
// ({"progress": ...}) and its result last; the row shows the step beside the
// elapsed seconds, then the result, one line per check. A plain JSON answer
// still works as before.
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

function el(tag, attrs, kids) {
  return {tag: tag, attrs: attrs || {}, kids: [], style: {}, textContent: (kids || []).join(''),
    classList: {add: function() {}, remove: function() {}},
    appendChild: function(c) { this.kids.push(c); },
    addEventListener: function(t, f) { this['on' + t] = f; }};
}
var cfg = {test_url: 'api/x/test', test_label: 'Test'}, current = {}, testRowEl = null;
function matchesShowWhen() { return true; }
function setInterval() { return 1; }
function clearInterval() {}

var reply = null;
function fetch() { return Promise.resolve(reply()); }
function streamed(lines) {
  var chunks = lines.map(function(l) { return new TextEncoder().encode(l); });
  return {ok: true, headers: {get: function() { return 'application/x-ndjson'; }},
    body: {getReader: function() {
      return {read: function() {
        return Promise.resolve(chunks.length ? {done: false, value: chunks.shift()} : {done: true});
      }};
    }}};
}

eval(lift(basics, 'function appendTestRow(host)', 'appendTestRow'));
var host = el('div');
appendTestRow(host);
var row = host.kids[0], btn = row.kids[0], out = row.kids[1];
function settled() { return new Promise(function(r) { setTimeout(r, 20); }); }

(async function() {
  // Split mid-line on purpose: a chunk boundary is not a line boundary.
  reply = function() {
    return streamed([
      '{"progress":"step 1 of 3: Connecting"}\n{"progress":"step 2',
      ' of 3: Tool call, then its result"}\n',
      '{"ok":false,"error":"Connected. The handoff check FAILED:\\n\\u2713 Tool call\\n\\u2717 Two calls"}\n']);
  };
  btn.onclick();
  await settled();
  check('the result replaces the progress', out.textContent.indexOf('✗ Connected. The handoff check FAILED:') === 0);
  check('the result keeps its lines', out.textContent.split('\n').length === 3);
  check('a multi-line result takes its own row', out.style.flexBasis === '100%');

  // While it runs, the step shows beside "Testing…".
  var hold;
  reply = function() {
    return {ok: true, headers: {get: function() { return 'application/x-ndjson'; }},
      body: {getReader: function() {
        var sent = false;
        return {read: function() {
          if (!sent) { sent = true; return Promise.resolve({done: false, value: new TextEncoder().encode('{"progress":"step 2 of 5: Two calls in one turn"}\n')}); }
          return new Promise(function(r) { hold = r; });
        }};
      }}};
  };
  btn.onclick();
  await settled();
  check('the running step shows', out.textContent === 'Testing… - step 2 of 5: Two calls in one turn');
  hold({done: true});
  await settled();
  check('a stream with no result line reads as failed', out.textContent.indexOf('✗') === 0);

  // Plain JSON, as every other test answers.
  reply = function() {
    return {ok: true, headers: {get: function() { return 'application/json'; }},
      json: function() { return Promise.resolve({ok: true, message: 'Connected'}); }};
  };
  btn.onclick();
  await settled();
  check('a JSON answer still reads', out.textContent === '✓ Connected');
  check('a one-line result sits beside the button', out.style.flexBasis === '');
  process.exit(fail ? 1 : 0);
})();
