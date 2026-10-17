// A form editing a saved record says when it holds unsaved changes, and
// leaving them behind asks first: closing the page, or the dialog it is in.

var fs = require('fs');
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}

check('only a form that loaded a record marks itself unsaved',
  /if \(cfg\.source\) \{\s*var markUnsaved = function\(\) \{ setUnsaved\(true\); \};/.test(basics));
check('the marker says leaving discards the changes',
  basics.indexOf("['Unsaved changes: leaving this page discards them']") >= 0);
check('a save clears it', basics.indexOf('setUnsaved(false);') >= 0);
check('leaving the page asks while one is unsaved',
  /addEventListener\('beforeunload', function\(ev\) \{\s*if \(!window\.uiHasUnsaved\(\)\) return;\s*ev\.preventDefault\(\);/.test(basics));
check('a form taken off the page stops counting',
  basics.indexOf('unsavedNodes = unsavedNodes.filter(function(n) { return n.isConnected; });') >= 0);
check('a dialog asks before closing over unsaved changes',
  prelude.indexOf("if (!discardOK && typeof window.uiUnsavedWithin === 'function' && window.uiUnsavedWithin(dlg)) {") >= 0 &&
  prelude.indexOf("window.uiConfirm('Discard your unsaved changes?')") >= 0);

process.exit(fail ? 1 : 0);
