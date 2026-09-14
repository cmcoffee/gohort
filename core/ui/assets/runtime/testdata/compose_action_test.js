// Kind:"compose" — a toolbar button that is really a macro.
//
// The point of the kind is what it does NOT do: it seeds the composer and
// stops, so the prompt is visible and amendable instead of being POSTed
// invisibly. These check the shipped source for the properties that make that
// true, because each one is a way to quietly turn it back into a fire-and-
// forget button.
var fs = require('fs');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var wb    = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('the panel publishes the seam',
  /window\.uiComposeMessage = function\(text, opts\)/.test(panel));

// The whole difference from a "report" action. If this ever calls sendMessage,
// the feature is gone and the button is a macro again.
var seam = panel.slice(panel.indexOf('window.uiComposeMessage'), panel.indexOf('uiRegisterMessageReplayHook'));
check('seeding does not send',
  seam.indexOf('sendMessage(') === -1);

check('the composer is focused and grown like a paste',
  /inputArea\.dispatchEvent\(new Event\('input'\)\);/.test(seam) && /inputArea\.focus\(\);/.test(seam));

// A selection means the next keystroke destroys the default the user is meant
// to amend.
check('the cursor lands after the text, not selecting it',
  /setSelectionRange\(end, end\)/.test(seam));

check('append keeps a half-written message',
  /if \(opts\.append && inputArea\.value\.trim\(\)\)/.test(seam));

check('the workbench dispatches the kind',
  /if \(a\.kind === 'compose'\) \{/.test(wb));

// A workbench with no chat column has no composer. Saying so beats a button
// that looks like it worked.
check('a page with no conversation says so',
  /typeof window\.uiComposeMessage !== 'function'/.test(wb));

// {id} goes into prose here, not a query string — encodeURIComponent would put
// %20 in the middle of a sentence.
check('the id is substituted raw, not URL-encoded',
  /a\.compose \|\| ''\)\.replace\('\{id\}', selectedId \|\| ''\)/.test(wb));

// --- a report's apply step ---------------------------------------------
//
// An apply that fires invisibly on one click is the blind regenerate a
// read-only report exists to avoid. The compose shape puts the instruction AND
// the findings in front of the author first.

check('a report can hand its apply to the composer',
  /if \(ap && ap\.compose\) \{/.test(wb));

check('the POST shape still works for a mechanical apply',
  /\} else if \(ap && ap\.url\) \{/.test(wb));

// A report synthesized from outside the app must not arrive as the author's own
// instruction. The server sends the fenced form; the runtime has to prefer it.
check('the fenced body wins over the raw report',
  /body: ap\.compose_body \|\| \(d && d\.report\) \|\| ''/.test(wb));

// The modal sits over the composer it just filled.
check('the report closes once its findings are in the composer',
  /mount: function\(body, dlg\) \{/.test(wb.slice(wb.indexOf("width: '720px'") - 80)));

// --- carrying a payload without burying the instruction -----------------

check('a large body becomes a paste marker',
  /body\.length >= pasteSnippetThreshold \? makePasteMarker\(body\) : body/.test(panel));

// The marker format is a contract with sendMessage's expansion regex; two
// hand-built copies would silently deliver the placeholder as literal text.
check('the marker is built in exactly one place',
  (panel.match(/'\[Pasted text #' \+/g) || []).length === 1);

check('the paste handler uses the shared builder',
  /var marker = makePasteMarker\(text\);/.test(panel));

process.exit(fail ? 1 : 0);
