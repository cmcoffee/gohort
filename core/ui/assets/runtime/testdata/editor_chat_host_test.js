// The code editor is its chat's host: every send carries the editor as it is
// on screen (the chat reads data-ui-send-context at send time), and a reply's
// editor_proposal card opens the proposed file as a diff in that editor.
//
// Extracts the shipped blocks from the runtime fragments and drives them with
// stubs, so it tests the code that ships rather than a copy of it.
var fs = require('fs');
var loop = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var cw = fs.readFileSync(__dirname + '/../50_code_editor_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function slice(src, from, to, what) {
  var a = src.indexOf(from), b = src.indexOf(to, a);
  if (a < 0 || b < 0) {
    console.log('FAIL ' + what + ' is gone or renamed');
    process.exit(1);
  }
  return src.slice(a, b + to.length);
}

// --- the chat reads its host's send context ---------------------------------
var hostFn = slice(loop, 'function hostSendContext() {', '\n    }\n', 'hostSendContext');
function hostContext(host) {
  var wrap = {closest: function(sel) { return sel === '[data-ui-send-context]' ? host : null; }};
  return new Function('wrap', hostFn + '\nreturn hostSendContext();')(wrap);
}
var asked = 0;
var got = hostContext({uiSendContext: function() { asked++; return {editor: {code: 'x = 1'}}; }});
check('the host is asked at send time', asked === 1);
check("the host's fields are what the send carries", got.editor && got.editor.code === 'x = 1');
check('no host adds nothing', Object.keys(hostContext(null)).length === 0);
check('a host that throws does not stop the send',
  Object.keys(hostContext({uiSendContext: function() { throw new Error('x'); }})).length === 0);
check('a host that answers nothing adds nothing',
  Object.keys(hostContext({uiSendContext: function() { return 'not an object'; }})).length === 0);
check('the send merges it into the body',
  loop.indexOf('Object.assign(body, hostSendContext());') >= 0);

// --- the editor is that host ------------------------------------------------
var hostBlock = slice(cw, "wrap.setAttribute('data-ui-send-context', '');", "        docSetValue(text);\n      }\n    };", 'the editor host block');
var attrs = {};
var wrap = {setAttribute: function(k, v) { attrs[k] = v; }};
var shown = null, set = null, raw = 0;
var editorStub = {};
new Function('wrap', 'nameInput', 'langSelect', 'editorValue', 'ctxEditor', 'pickedCollections',
  'selectedRef', 'pickedProfile', 'docToRaw', 'editorWrap', 'editor', 'docSetValue', 'window',
  hostBlock)(
  wrap, {value: '  report.sql  '}, {value: 'sql'}, function() { return 'select 1'; }, {value: 'schema notes'},
  function() { return ['c1']; }, {kind: 'system', item_id: 'db01'}, 'house-style',
  function() { raw++; }, {}, editorStub, function(t) { set = t; },
  {editorShowDiff: function(o) { shown = o; }});
check('the editor marks itself as its chat\'s host', 'data-ui-send-context' in attrs);
var ctx = wrap.uiSendContext();
check('a send carries the unsaved code', ctx.editor.code === 'select 1');
check('and the name, trimmed', ctx.editor.name === 'report.sql');
check('and the language', ctx.editor.lang === 'sql');
check('and the context block', ctx.editor.context === 'schema notes');
check('and the picked collections', ctx.editor.collections.length === 1 && ctx.editor.collections[0] === 'c1');
check('and the picked source', ctx.editor.references.length === 1 && ctx.editor.references[0].item_id === 'db01');
check('and the profile in force', ctx.editor.profile === 'house-style');
wrap.uiProposeEdit('select 2');
check('a proposal leaves outline mode first', raw === 1);
check('a proposal opens as a diff against the editor', shown && shown.newText === 'select 2' && shown.editorTextarea === editorStub);
check('nothing is applied until Apply', set === null);
shown.onApply('select 2');
check('Apply puts the proposal in the editor', set === 'select 2');

// --- the editor_proposal card -----------------------------------------------
var reg = slice(cw, "window.uiRegisterBlockRenderer('editor_proposal'", "    return {wrap: card};\n  });", 'the editor_proposal renderer');
var renderer = null;
function el(tag, attrs, kids) {
  var n = {tag: tag, attrs: attrs || {}, kids: kids || [], children: []};
  n.appendChild = function(c) { n.children.push(c); };
  n.closest = function() { return n.host || null; };
  return n;
}
var timers = [];
new Function('window', 'el', 'setTimeout', reg)(
  {uiRegisterBlockRenderer: function(name, fn) { if (name === 'editor_proposal') renderer = fn; }},
  el, function(fn) { timers.push(fn); });
check('the card renderer is registered', typeof renderer === 'function');
var proposed = [];
var host = {uiProposeEdit: function(t) { proposed.push(t); }};
var d = {type: 'editor_proposal', id: 'p1', text: 'Add a WHERE clause', data: {code: 'select 1\nwhere x'}};

var live = renderer(d, {live: true});
check('the card is drawn', live && live.wrap);
live.wrap.host = host;
timers.splice(0).forEach(function(fn) { fn(); });
check('live, the diff opens at once', proposed.length === 1 && proposed[0] === 'select 1\nwhere x');
check('the card says what the change is', live.wrap.children[0].kids[0] === 'Add a WHERE clause');
check('and how long the file is', live.wrap.children[1].kids[0] === '2 lines');

proposed = [];
var replay = renderer(d, {});
replay.wrap.host = host;
timers.splice(0).forEach(function(fn) { fn(); });
check('a reload does not open the diff unasked', proposed.length === 0);
replay.wrap.children[2].attrs.onclick();
check('Review opens it again', proposed.length === 1);

// The stream marks its blocks live; a reload's replay goes through the same
// dispatcher without it.
check('a streamed block is marked live', loop.indexOf("addBlock(ev, true);") >= 0);
check('the renderer is told', loop.indexOf('live: !!live}') >= 0);

process.exit(fail ? 1 : 0);
