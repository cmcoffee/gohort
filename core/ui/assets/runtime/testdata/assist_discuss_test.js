// The assist window's discuss mode: talk it through, then write once.
//
// The server's reply contract decides when text comes back; the window's part
// is to say which contract it wants on every send, and to offer a way to stop
// discussing and have it written.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../06_assist_editor.js', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}

check('discuss mode is opt-in: pinned by the caller, or offered by a switchable window',
  src.indexOf("var switchable = !!opts.switchable && !opts.mode;") >= 0 &&
  src.indexOf("var discuss = opts.mode === 'discuss' ||") >= 0);
check('every send says which mode it is in',
  src.indexOf("opts.send({message: text, draft: draft.value, history: priorHistory, mode: discuss ? 'discuss' : 'write'}") >= 0);
check('Write it now exists only where discussing is possible, shows only while discussing, and sends as a message',
  src.indexOf("var writeBtn = (discuss || switchable) ?") >= 0 &&
  src.indexOf("writeBtn.style.display = discuss ? '' : 'none';") >= 0 &&
  src.indexOf("writeBtn.addEventListener('click', function() { send(writeNowText); });") >= 0);
check('Write it now is disabled while a reply is pending', src.indexOf('if (writeBtn) writeBtn.disabled = on;') >= 0);

process.exit(fail ? 1 : 0);
