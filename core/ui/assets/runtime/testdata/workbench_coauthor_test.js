// A workbench whose document is co-authored (propose_edits): the open record
// is its editor from the start, an empty one for a record with no body yet,
// every chat send carries the document as it is on screen, and the editor is
// a uiCoauthor document that goes away with the editor.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}
function lift(start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}

// --- what the editor opens on ----------------------------------------------
var editableSrc = lift('function editableText(rec)', 'editableText');
function editable(cfg, rec) {
  return new Function('cfg', 'editField', editableSrc + '\nreturn editableText;')(cfg, 'lyrics')(rec);
}
check('a record\'s own text is what it edits', editable({}, {lyrics: 'la la'}) === 'la la');
check('a plain workbench offers nothing to edit for a record without the field', editable({}, {title: 'x'}) === null);
check('a co-authored one opens a record without it as an empty document', editable({propose_edits: true}, {title: 'x'}) === '');
check('but not a field that holds something other than text', editable({propose_edits: true}, {lyrics: 42}) === null);
check('no record, nothing', editable({propose_edits: true}, null) === null);

// --- what a chat send carries ------------------------------------------------
var a = src.indexOf('if (cfg.propose_edits) {\n      root.setAttribute(\'data-ui-send-context\'');
var b = src.indexOf('\n    }\n', a);
if (a < 0 || b < 0) { console.log('FAIL the send context block is gone'); process.exit(1); }
var block = src.slice(a, b + 6);
function host(state) {
  var root = {attrs: {}, setAttribute: function(k, v) { this.attrs[k] = v; }};
  new Function('cfg', 'root', 'state', 'editableText',
    'var selectedId = state.selectedId, editing = state.editing, editorTA = state.editorTA, lastRec = state.lastRec;\n' + block)(
    {propose_edits: true}, root, state, function(rec) { return editable({propose_edits: true}, rec); });
  return root;
}
var r = host({selectedId: 'song1', editing: true, editorTA: {value: 'typed, not saved'}, lastRec: {lyrics: 'saved'}});
check('the workbench is its chat\'s host', 'data-ui-send-context' in r.attrs);
var ctx = r.uiSendContext();
check('a send carries the open document as it is on screen, unsaved edits and all',
  ctx.document && ctx.document.id === 'song1' && ctx.document.text === 'typed, not saved');
check('with the editor closed, the saved text',
  host({selectedId: 'song1', editing: false, lastRec: {lyrics: 'saved'}}).uiSendContext().document.text === 'saved');
check('nothing open sends nothing', Object.keys(host({selectedId: null}).uiSendContext()).length === 0);

// --- the editor is co-authored ---------------------------------------------
var open = lift('function openEditor()', 'openEditor');
check('the editor becomes a uiCoauthor document with the page\'s quick actions',
  /coauthor = window\.uiCoauthor\(ta, \{\s*host: root,\s*actions: cfg\.selection_actions \|\| \[\],/.test(open));
check('on a phone, a proposal brings the document up',
  open.indexOf("showDocument: function() { panes.show('viewer'); }") >= 0);
check('only when the document is co-authored', open.indexOf('if (cfg.propose_edits && window.uiCoauthor) {') >= 0);
check('and it goes when the editor does',
  lift('function closeEditor()', 'closeEditor').indexOf('if (coauthor) { coauthor.destroy(); coauthor = null; }') >= 0);
check('a co-authored document opens straight into its editor',
  lift('function loadViewer(id)', 'loadViewer').indexOf('if (cfg.propose_edits && cfg.edit_url) openEditor();') >= 0);

check('a co-authored document has no Edit toggle beside its editor',
  src.indexOf('var editToggle = cfg.edit_url && !cfg.propose_edits;') >= 0 && src.indexOf('if (editToggle) {') >= 0);
check('its Cancel is Revert, which asks before putting the saved text back',
  open.indexOf("text: cfg.propose_edits ? 'Revert' : 'Cancel'") >= 0 &&
  open.indexOf("window.uiConfirm('Put back the saved text? What you changed since the last Save goes.')") >= 0 &&
  open.indexOf('closeEditor(); renderRecord(lastRec); openEditor();') >= 0);
check('and Escape never discards it',
  open.indexOf("else if (ev.key === 'Escape' && !cfg.propose_edits)") >= 0);
check('a document read rather than written in place can be highlighted to talk about it',
  src.indexOf('if (cfg.highlight_ask && !cfg.propose_edits && window.uiHighlightContext) {') >= 0 &&
  src.indexOf('window.uiHighlightContext(viewerBody, {host: root, actions: cfg.selection_actions || []});') >= 0);
process.exit(fail ? 1 : 0);
