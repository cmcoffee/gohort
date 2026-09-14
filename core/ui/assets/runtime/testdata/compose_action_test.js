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

var seam = panel.slice(panel.indexOf('window.uiComposeMessage'), panel.indexOf('uiRegisterMessageReplayHook'));

// Sending is the CALLER's call. A chooser already asked what the user wanted
// and has no business asking twice; a control that seeded a default unprompted
// should let them look at it first.
check('seeding does not send by default',
  /if \(opts\.send\) sendMessage\(\);/.test(seam));

// After the cursor work, so a send that fails visibly leaves the composer in
// the state the user would retry from.
check('the send comes last',
  seam.indexOf('setSelectionRange(end, end)') < seam.indexOf('if (opts.send)'));

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
  /var fill = function\(t\) \{ return \(t \|\| ''\)\.replace\('\{id\}', selectedId \|\| ''\); \};/.test(wb));

// Every text that reaches the composer goes through the same substitution —
// the default option, a free-text row's seed, and the no-options path alike.
check('option text is substituted too',
  /msg = o\.input \? \(box\.value \|\| ''\)\.trim\(\) : fill\(o\.text\)/.test(wb) &&
  /box\.value = fill\(o\.text\) \|\| '';/.test(wb));

// --- a report's apply step ---------------------------------------------
//
// An apply that fires invisibly on one click is the blind regenerate a
// read-only report exists to avoid. The compose shape puts the instruction AND
// the findings in front of the author first.

check('a report can hand its apply to the chat',
  /if \(ap && ap\.compose\) \{/.test(wb));

// The report modal IS the review — the findings are above the button. Parking a
// copy in the composer would ask the author to agree to the same thing twice.
check('the apply sends',
  /\{body: ap\.compose_body \|\| \(d && d\.report\) \|\| '', send: true\}/.test(wb));

// The send starts a turn and scrolls the conversation; a report still sitting
// over it hides the work the click just started.
var applyBlock = wb.slice(wb.indexOf('if (ap && ap.compose)'), wb.indexOf('} else if (ap && ap.url)'));
check('the report closes before the send, not after',
  applyBlock.indexOf('dlg.remove()') < applyBlock.indexOf('window.uiComposeMessage(ap.compose'));

check('the POST shape still works for a mechanical apply',
  /\} else if \(ap && ap\.url\) \{/.test(wb));

// A report synthesized from outside the app must not arrive as the author's own
// instruction. The server sends the fenced form; the runtime has to prefer it.
check('the fenced body wins over the raw report',
  /body: ap\.compose_body \|\| \(d && d\.report\) \|\| ''/.test(wb));

check('the report modal has a handle to close itself with',
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

// --- the chooser --------------------------------------------------------
//
// Seeding alone taxed every use of a macro to buy an edit that is wanted
// rarely. With options the default is one click and the escape hatch is a
// visible row rather than a pre-filled box you have to notice is editable.

check('options turn the action into a chooser',
  /if \(a\.compose_options && a\.compose_options\.length\) \{/.test(wb));

check('no options still seeds and stops',
  /if \(!window\.uiComposeMessage\(fill\(a\.compose\)\)\)/.test(wb));

check('the chosen option is sent, not parked in the composer',
  /window\.uiComposeMessage\(msg, \{send: true\}\)/.test(wb));

// A free-text row seeded from its own text: "like the default but…" should be
// an edit, not a retype.
check('a free-text row is seeded once per selection',
  /if \(box\.dataset\.seededFor !== String\(picked\)\)/.test(wb));

// Re-seeding on every sync would wipe what the user just typed.
check('re-selecting the same row does not wipe the edit',
  /box\.dataset\.seededFor = String\(picked\);/.test(wb));

check('Enter sends, matching the composer it feeds',
  /if \(ev\.key === 'Enter' && !ev\.shiftKey\) \{ ev\.preventDefault\(\); go\(\); \}/.test(wb));

// An empty free-text box is a mistake, not an instruction.
check('an empty box does not send',
  /if \(!msg\) \{ box\.focus\(\); return; \}/.test(wb));

process.exit(fail ? 1 : 0);
