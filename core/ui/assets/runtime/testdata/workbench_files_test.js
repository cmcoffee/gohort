// The files a workbench record keeps (files_field), under the document: each
// played or shown by its kind, newest first, a list a chat round's refresh
// leaves alone when nothing changed (so a take being played keeps playing),
// and one removed only after asking.
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

// --- how each kind is shown --------------------------------------------------
var kind = new Function(lift('function wbFileKind(type)', 'wbFileKind') + '\nreturn wbFileKind;')();
check('a song plays', kind('audio/mpeg') === 'audio');
check('a clip plays', kind('video/mp4') === 'video');
check('a picture shows', kind('image/png') === 'image');
check('anything else opens', kind('application/pdf') === 'other' && kind(undefined) === 'other');

// --- the list ------------------------------------------------------------------
function fakeNode() {
  var n = {children: [], style: {}, appendChild: function(c) { this.children.push(c); return c; }};
  Object.defineProperty(n, 'innerHTML', {set: function() { n.children = []; }});
  return n;
}
var built = [];
var center = {cls: {}, classList: {toggle: function(c, on) { center.cls[c] = on; }}};
var render = new Function('cfg', 'filesBox', 'state', 'el', 'fileRow', 'center',
  'var filesShown = "";\n' +
  'var selectedId;\n' +
  lift('function renderFiles(rec)', 'renderFiles').replace('function renderFiles(rec) {', 'function renderFiles(rec) { selectedId = state.selectedId;') +
  '\nreturn renderFiles;');
var box = fakeNode();
var state = {selectedId: 's1'};
var renderFiles = render({files_field: 'takes', files_label: 'Takes'}, box, state,
  function(tag, opts) { var n = fakeNode(); n.opts = opts || {}; return n; },
  function(f) { built.push(f.name); return {name: f.name}; }, center);

renderFiles({takes: [{name: 'a.mp3'}, {name: 'b.mp3'}]});
check('shown, newest first', box.style.display === '' && built.join(',') === 'b.mp3,a.mp3');
check('headed with the page\'s name for them and how many', box.children[0].opts.text === 'Takes (2)');
check('and the editor makes room for it', center.cls['ui-wb-has-files'] === true);
built = [];
renderFiles({takes: [{name: 'a.mp3'}, {name: 'b.mp3'}], lyrics: 'edited'});
check('a refresh with the same takes leaves the players alone', built.length === 0);
renderFiles({takes: [{name: 'a.mp3'}, {name: 'b.mp3'}, {name: 'c.mp3'}]});
check('a new take rebuilds the list', built.join(',') === 'c.mp3,b.mp3,a.mp3');
built = [];
state.selectedId = 's2';
renderFiles({takes: [{name: 'a.mp3'}]});
check('another song with a same-named take is its own list', built.join(',') === 'a.mp3');
renderFiles({lyrics: 'none kept'});
check('a record with none hides the strip', box.style.display === 'none' && box.children.length === 0);
check('and gives the editor its height back', center.cls['ui-wb-has-files'] === false);
renderFiles(null);
check('so does nothing open', box.style.display === 'none');

// --- each row ------------------------------------------------------------------
var row = lift('function fileRow(f)', 'fileRow');
check('a take plays in place without fetching until asked',
  row.indexOf("el('audio', {class: 'ui-wb-file-media', controls: true, preload: 'none', src: url})") >= 0);
check('its URL names the open record and the file',
  row.indexOf("cfg.file_url.replace('{id}', encodeURIComponent(selectedId)).replace('{name}', encodeURIComponent(f.name))") >= 0);
check('remove asks first, then deletes and re-reads the record',
  row.indexOf('window.uiConfirm(') >= 0 && row.indexOf("fetch(url, {method: 'DELETE'") >= 0 && row.indexOf('loadViewer(selectedId)') >= 0);

// --- when it is drawn --------------------------------------------------------
var load = lift('function loadViewer(id)', 'loadViewer');
check('every fetch of the record draws it, the editor open or not',
  load.indexOf('renderFiles(rec);') >= 0 && load.indexOf('renderFiles(rec);') < load.indexOf('if (editing && !switched) return;'));
check('closing the record clears it', lift('function showEmpty()', 'showEmpty').indexOf('renderFiles(null);') >= 0);
check('only on a workbench whose records keep files', src.indexOf('if (cfg.files_field && cfg.file_url) {') >= 0);
process.exit(fail ? 1 : 0);
