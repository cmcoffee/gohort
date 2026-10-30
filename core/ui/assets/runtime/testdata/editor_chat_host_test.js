// The code editor is its chat's host: every send carries the editor as it is
// on screen (the chat reads data-ui-send-context at send time), and what a
// reply proposes is a text_proposal, reviewed as a co-authored document. An
// editor_proposal stored before that draws as one.
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
var hostBlock = slice(cw, "wrap.setAttribute('data-ui-send-context', '');", "      }};\n    };", 'the editor host block');
var attrs = {};
var wrap = {setAttribute: function(k, v) { attrs[k] = v; }};
new Function('wrap', 'nameInput', 'langSelect', 'editorValue', 'ctxEditor', 'pickedCollections',
  'selectedRef', 'pickedProfile', hostBlock)(
  wrap, {value: '  report.sql  '}, {value: 'sql'}, function() { return 'select 1'; }, {value: 'schema notes'},
  function() { return ['c1']; }, {kind: 'system', item_id: 'db01'}, 'house-style');
check('the editor marks itself as its chat\'s host', 'data-ui-send-context' in attrs);
var ctx = wrap.uiSendContext();
check('a send carries the unsaved code', ctx.editor.code === 'select 1');
check('and the name, trimmed', ctx.editor.name === 'report.sql');
check('and the language', ctx.editor.lang === 'sql');
check('and the context block', ctx.editor.context === 'schema notes');
check('and the picked collections', ctx.editor.collections.length === 1 && ctx.editor.collections[0] === 'c1');
check('and the picked source', ctx.editor.references.length === 1 && ctx.editor.references[0].item_id === 'db01');
check('and the profile in force', ctx.editor.profile === 'house-style');

// --- what it proposes is reviewed as a co-authored document -------------------
check('the editor is a uiCoauthor document', cw.indexOf('window.uiCoauthor(editor, {\n      host: wrap,') >= 0);
check('a proposal leaves outline mode first',
  cw.indexOf("showDocument: function() { docToRaw(); panes.show('code'); },") >= 0);
check('Apply writes the editor through the outline too',
  cw.indexOf('setValue: function(t) { docToRaw(); docSetValue(t); },') >= 0);
check('there is one review: the old diff pane is gone', cw.indexOf('editorShowDiff') < 0 && cw.indexOf('uiProposeEdit') < 0);

// --- an editor_proposal from before is a text_proposal --------------------------
var reg = slice(cw, "window.uiRegisterBlockRenderer('editor_proposal'", "\n  });", 'the editor_proposal renderer');
var renderer = null, seen = null;
var win = {
  uiRegisterBlockRenderer: function(name, fn) { if (name === 'editor_proposal') renderer = fn; },
  UIBlockRenderers: {text_proposal: function(d, c) { seen = {d: d, c: c}; return {wrap: 'card'}; }},
};
new Function('window', reg)(win);
check('the old card type is still registered', typeof renderer === 'function');
var out = renderer({type: 'editor_proposal', id: 'p1', text: 'Add a WHERE clause', data: {code: 'select 1\nwhere x', lang: 'sql'}}, {live: true});
check('it draws the text_proposal card', out && out.wrap === 'card');
check('for the whole file',
  seen && seen.d.type === 'text_proposal' && seen.d.data.find === '' && seen.d.data.replace === 'select 1\nwhere x' && seen.d.text === 'Add a WHERE clause');
check('live or replayed as it was', seen.c.live === true);
win.UIBlockRenderers = {};
check('and nothing without the co-author runtime', renderer({data: {code: 'x'}}, {}) === null);

// The stream marks its blocks live; a reload's replay goes through the same
// dispatcher without it.
check('a streamed block is marked live', loop.indexOf("addBlock(ev, true);") >= 0);
check('the renderer is told', loop.indexOf('live: !!live}') >= 0);

process.exit(fail ? 1 : 0);
