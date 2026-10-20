// A paste into the chat composer attaches what the paperclip would: a pasted
// screenshot (which some browsers hand over only as a file ITEM), and a PDF,
// document or audio file copied in the Finder, whose name rides along as
// text and must not be what gets pasted. Into the panel's own queue, not the
// page hook a second panel overwrites.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}
var from = src.indexOf('    function pastedFiles(clip) {');
var to = src.indexOf('    var attachInput = null, attachBtn = null;');
if (from < 0 || to < from) { console.log('FAIL the paste helpers moved'); process.exit(1); }
function lift(transcribe) {
  return new Function('window', src.slice(from, to) + '\nreturn {pastedFiles: pastedFiles, attachAccepts: attachAccepts};')({GOHORT_TRANSCRIBE_ENABLED: transcribe});
}
var h = lift(false);
var pdf = {name: 'brief.pdf', type: 'application/pdf'};
var shot = {name: '', type: 'image/png'};

check('a Finder copy attaches the file', h.pastedFiles({files: [pdf], items: []})[0] === pdf);
check('a screenshot only in the items is found',
  h.pastedFiles({files: [], items: [{kind: 'string', type: 'text/plain'}, {kind: 'file', type: 'image/png', getAsFile: function() { return shot; }}]})[0] === shot);
check('plain text is no file', h.pastedFiles({files: [], items: [{kind: 'string', type: 'text/plain'}]}).length === 0);

check('a PDF is accepted', h.attachAccepts(pdf));
check('a screenshot is accepted', h.attachAccepts(shot));
check('a Word document is accepted', h.attachAccepts({name: 'notes.docx', type: ''}));
check('a Markdown file with no type is accepted by its name', h.attachAccepts({name: 'README.md', type: ''}));
check('a zip is refused', !h.attachAccepts({name: 'logs.zip', type: 'application/zip'}));
check('audio is refused without transcription', !h.attachAccepts({name: 'memo.m4a', type: 'audio/mp4'}));
check('audio is accepted with transcription', lift(true).attachAccepts({name: 'memo.m4a', type: 'audio/mp4'}));

var paste = src.slice(src.indexOf("inputArea.addEventListener('paste'"), src.indexOf('function queueAttachment(file)'));
check('the paste queues into the panel itself', /queueAttachment\(file\)/.test(paste) && !/window\.uiAddPendingAttachment\(/.test(paste));
check('a file in the paste wins over its name as text', paste.indexOf('pastedFiles(clip)') < paste.indexOf("clip.getData('text/plain')"));
check('the paperclip and the paste share one reader', /attachInput\.addEventListener\('change'[\s\S]{0,120}forEach\(queueAttachment\)/.test(src));

console.log(fail === 0 ? '\nALL PASTE TESTS PASS' : '\n' + fail + ' FAILURES');
process.exit(fail ? 1 : 0);
