// A list-level button has to be able to REACH its handler.
//
// fireViewAction is declared once and called once, from the onclick of every
// view_actions button. Those two lines live in different places: the button is
// built in renderOrchTable, and when the row-painting body was split out into
// paintOrchRows the handler was carried along with it. A function declaration
// is scoped to the function it is written in, so the call could no longer see
// it and every list-level button threw a ReferenceError and did nothing.
//
// Nothing else caught that. The bundle still parses, Go still builds, and the
// only symptom is a button that looks fine and is dead: "New recurring task"
// and "New machine run" on the Scheduler, the two creators that exist so the
// page listing schedules is the page that adds one.
//
// So the shape is pinned rather than the text: fireViewAction is a SIBLING of
// paintOrchRows, both direct children of renderOrchTable. Comparing the two
// indents rather than matching a fixed one survives a reindent of the file and
// still fails the moment one is nested inside the other.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

function declIndent(name) {
  var m = new RegExp('\\n( *)function ' + name + '\\(').exec(src);
  return m ? m[1].length : -1;
}

var view = declIndent('fireViewAction');
var paint = declIndent('paintOrchRows');

check('fireViewAction is declared', view >= 0);
check('paintOrchRows is declared', paint >= 0);
check('the view-action handler is a sibling of the row painter, not nested in it',
  view >= 0 && view === paint);

// The call the scope has to satisfy. If this moves, the check above is
// measuring the wrong pair and should move with it.
check('view-action buttons call it from the list-level bar',
  /onclick: function\(\) \{ fireViewAction\(a, reload\); \}/.test(src));

// A row action's helpers are the other half of the same rule: they are called
// only from the row renderers, so they belong INSIDE the painter. Pinned so a
// later tidy-up does not "fix" the sibling rule by hoisting everything.
check('row-action helpers stay with the rows they serve',
  declIndent('openRowPicker') > paint && declIndent('fireRowAction') > paint);

process.exit(fail ? 1 : 0);
