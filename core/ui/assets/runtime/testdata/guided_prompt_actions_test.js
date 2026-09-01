// Guided toolbar actions: a button that SAYS something instead of calling
// something, and a panel that can be a pane rather than a page.
//
// Both exist for the same reason — a conversation mounted inside something
// else. A viewport-tall chat buries the row it belongs to, and an empty
// composer asks the reader to guess the wording of a job the page already
// knows the shape of.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var act = src.indexOf('async function runToolbarAction(action, btn) {');
check('the toolbar handler exists', act > 0);
var body = src.slice(act, act + 1400);

// The prompt is sent as the user's turn, through the ordinary composer — so a
// button and a person typing the same sentence produce the same conversation.
check('a prompt action goes through the composer',
  /inputArea\.value = action\.prompt;/.test(body) && /sendMessage\(\);/.test(body));

// Before the method switch: an action carrying a prompt has already said what
// it does, and must never also POST somewhere.
check('a declared prompt short-circuits the method',
  body.indexOf('action.prompt') < body.indexOf("var method = action.method"));

// A half-typed draft is not the framework's to throw away.
check('the draft is restored afterwards',
  /var draft = inputArea\.value;/.test(body) && /inputArea\.value = draft;/.test(body));

// A confirm still runs first — a guided action is an ordinary action.
check('confirm is still honoured',
  body.indexOf('uiConfirm(action.confirm)') < body.indexOf('action.prompt'));

// cfg.height: the panel as a part of a page.
check('height overrides the viewport-tall default',
  /if \(cfg\.height\) \{ wrap\.style\.height = cfg\.height;/.test(src));
check('and releases the min-height with it',
  /wrap\.style\.minHeight = '0';/.test(src));

process.exit(fail ? 1 : 0);
