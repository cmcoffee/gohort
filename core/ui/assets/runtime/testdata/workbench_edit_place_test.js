// Where a workbench's Edit toggle sits in its toolbar.
//
// It leads the bar by default. An app that wants something ahead of it (a
// document's own Settings) marks the toggle's place with an action of kind
// "edit", which is not a button of its own.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');

var fail = 0;
function check(name, cond) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name);
}

check('a marker of kind "edit" is looked for',
  src.indexOf("var editPlaced = (cfg.viewer_actions || []).some(function(a) { return a.kind === 'edit'; });") >= 0);
check('without one, the toggle leads the bar',
  src.indexOf('if (!editPlaced) actionBar.appendChild(editBtn);') >= 0);
check('with one, the toggle goes where it is and the marker draws nothing else',
  /if \(a\.kind === 'edit'\) \{\s*if \(editBtn\) actionBar\.appendChild\(editBtn\);\s*return;\s*\}/.test(src));

process.exit(fail ? 1 : 0);
