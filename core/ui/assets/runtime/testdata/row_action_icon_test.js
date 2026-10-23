// A row button can carry a small drawn mark before its label (action.icon:
// play, pause, once). Drawn rather than an emoji, so it sits on the text's
// line and takes the button's colour; the label always stays beside it, and an
// unknown name draws nothing. Both row layouts (cards and table) use it.
// Checked against the shipped source and styles.
var fs = require('fs');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var g = prelude.slice(prelude.indexOf('var UI_ACTION_SHAPES = {'), prelude.indexOf('window.uiActionGlyph = uiActionGlyph;'));
['play:', 'pause:', 'once:'].forEach(function(n) { check('the ' + n.slice(0, -1) + ' mark is drawn', g.indexOf(n) > 0); });
check('an unknown name draws nothing', g.indexOf('if (!shapes) return null;') > 0);
check('it takes the button colour', g.indexOf("svg.setAttribute('fill', 'currentColor');") > 0);
check('it is exported for apps', prelude.indexOf('window.uiActionGlyph = uiActionGlyph;') > 0);

var helper = panel.slice(panel.indexOf('function rowActionLabel(a) {'), panel.indexOf('function rowActionLabel(a) {') + 220);
check('the label stays beside the mark', helper.indexOf('return g ? [g, a.label] : [a.label];') > 0);
check('both row layouts use it', panel.split('rowActionLabel(a));').length - 1 === 2);
check('the mark sits on the text line', css.indexOf('.ui-row-btn .ui-action-glyph {') >= 0);

process.exit(fail ? 1 : 0);
