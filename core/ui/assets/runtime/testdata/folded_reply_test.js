// A reply the one after it replaced (a draft the review sent back) is kept on
// the record but folded behind its line, with a control to open it, and is not
// struck: nothing in it was ruled untrue. Checked against the shipped source
// and styles.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var fn = src.slice(src.indexOf('function foldMessage(id, line, detail) {'), src.indexOf('window.uiSetMessageBadge = function'));
check('the fold renderer exists', fn.length > 0 && src.indexOf('function foldMessage(id, line, detail) {') > 0);
check('the server event reaches it', src.indexOf("case 'chunk_folded':") > 0 && src.indexOf("foldMessage(ev.id, ev.line || '', ev.detail || '');") > 0);
check('a reload folds it again', src.indexOf("if (meta.folded && meta.folded.line) foldMessage(id, meta.folded.line, meta.folded.detail || '');") > 0);
check('folded until opened', fn.indexOf("m.bubble.classList.toggle('ui-agent-msg-folded', !m.foldOpen);") > 0);
check('the open state survives a repaint', fn.indexOf('m.foldOpen = !m.foldOpen; paint();') > 0);
check('not struck', fn.indexOf('ui-agent-msg-struck') < 0);
check('no line, nothing folded', /if \(!line\) \{[\s\S]{0,120}classList\.remove\('ui-agent-msg-folded'\);/.test(fn));
check('the copy says it was folded', src.indexOf("(folded ? ', folded' :") > 0);
check('the folded body is hidden', css.indexOf('.ui-agent-msg-folded .ui-agent-msg-body { display: none; }') >= 0);

// The reload: setMessageMeta could fold, but no replay path handed it the
// fold, so a reopened thread showed the sent-back draft open above its
// revision. Every replay goes through savedMessageMeta, and it carries folded.
var start = src.indexOf('function savedMessageMeta(m) {');
check('one helper builds a saved message\'s meta', start > 0);
var body = src.slice(start, src.indexOf('\n    }\n', start) + 6);
var savedMessageMeta = new Function(body + '\nreturn savedMessageMeta;')();
var fold = {line: 'Draft, sent back by the review.', detail: 'why'};
var got = savedMessageMeta({role: 'assistant', content: 'draft', folded: fold});
check('a folded message alone still gets its meta', !!got && got.folded === fold);
check('a message with nothing to replay gets none', savedMessageMeta({role: 'assistant', content: 'x'}) === null);
check('a fold with no line is not a fold', savedMessageMeta({folded: {line: ''}}) === null);
var calls = src.match(/setMessageMeta\(mid, [^)]*\)/g) || [];
check('every replay path uses it (' + calls.length + ' found)', calls.length >= 3 && calls.every(function(c) { return c === 'setMessageMeta(mid, meta)'; }));

// A voice reading the draft has to hear it was folded: said once, as it folds.
check('folding says so to listeners, once', fn.indexOf('if (line && !m.folded) announceText(m, id, {folded: true});') > 0 &&
  fn.indexOf('announceText') < fn.indexOf("m.folded = line || '';"));
check('and the event carries it', src.indexOf('struck: !!what.struck, folded: !!what.folded,') > 0);

process.exit(fail ? 1 : 0);
