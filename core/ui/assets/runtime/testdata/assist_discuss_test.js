// The assist window's discuss mode: talk it through, then write once.
//
// The server's reply contract decides when text comes back; the window's part
// is to say which contract it wants on every send. Going ahead is a question
// the assistant asks, answered with its choices (TestAssistChoices).

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
// The conversation pane is shared (uiAssistChat): it builds every request,
// and the draft window adds its draft through extra.
check('every send says which mode it is in',
  src.indexOf("var req = {message: text, history: priorHistory, mode: discuss ? 'discuss' : 'write', window: windowId};") >= 0 &&
  src.indexOf("opts.send(req, finish,") >= 0);
check('the draft window sends its draft with every turn',
  src.indexOf("extra: function() { return {draft: draft.value}; },") >= 0);
process.exit(fail ? 1 : 0);
