package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A row action with Line 2 leads the row's second line, under the name and
// ahead of that line's text, outside the text's muted wrap so a control is
// never dimmed; it is no longer among the buttons at the row's end. A row
// whose Line 2 actions all hide (OnlyIf) keeps the layout it had, and a Cards
// table ignores Line.
func TestARowActionCanLeadTheSecondLine(t *testing.T) {
	if raw, _ := json.Marshal(RowAction{Type: "segmented", Line: 2}); !strings.Contains(string(raw), `"line":2`) {
		t.Fatalf("line is not on the wire: %s", raw)
	}
	if raw, _ := json.Marshal(RowAction{Type: "button"}); strings.Contains(string(raw), `"line"`) {
		t.Errorf("an action with no Line carries one: %s", raw)
	}
	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "if (!cfg.cards && Number(act.line) === 2) return; // on the second line") {
		t.Error("a Line 2 action is still drawn among the row's end buttons")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	start, end := strings.Index(src, "        var lineActs = null;"), strings.Index(src, "        } else row.appendChild(cellsWrap);")
	if start < 0 || end < start {
		t.Fatal("the second line's build is not found")
	}
	harness := `
function el(tag, opts, kids) {
  return {cls: (opts && opts['class']) || '', kids: (kids || []).slice(), childNodes: [],
    appendChild: function(k) { this.kids.push(k); this.childNodes.push(k); return k; }};
}
function appendAction(parent, act) {
  if (act.only_if && !rec[act.only_if]) return;
  parent.appendChild({cls: 'act:' + act.label, kids: []});
}
function shape(n) { return n.cls + (n.kids.length ? '[' + n.kids.map(shape).join(' ') + ']' : ''); }
var rec, rowKey = 'name';
function build(cfg, r, withText) {
  rec = r;
  var row = el('div', {'class': 'row'});
  var cellsWrap = el('div', {'class': 'cells'});
  var secondLine = withText ? el('div', {'class': 'text2'}) : null;
` + src[start:end] + `
  } else row.appendChild(cellsWrap);
  return shape(row);
}
function want(got, exp, why) { if (got !== exp) { console.log('FAIL ' + why + ': ' + got); process.exitCode = 1; } }
var acts = [{label: 'role', line: 2}, {label: 'uses', line: 2, only_if: 'both'}, {label: 'Settings'}, {label: 'lead', leading: true, line: 2}];
want(build({row_actions: acts}, {both: true}, true),
  'row[ui-row-lines[cells ui-row-line-2[ui-row-line-actions[act:role act:uses] text2]]]', 'line 2 actions lead the text');
want(build({row_actions: acts}, {both: false}, false),
  'row[ui-row-lines[cells ui-row-line-2[ui-row-line-actions[act:role]]]]', 'a row with no second-line text still gets the line');
want(build({row_actions: [{label: 'uses', line: 2, only_if: 'both'}]}, {both: false}, true),
  'row[ui-row-lines[cells text2]]', 'all hidden: the line as it was');
want(build({row_actions: [{label: 'uses', line: 2, only_if: 'both'}]}, {both: false}, false),
  'row[cells]', 'all hidden, no text: one line');
want(build({cards: 'list', row_actions: acts}, {both: true}, true),
  'row[ui-row-lines[cells text2]]', 'a card ignores Line');
if (!process.exitCode) console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "line2.js")
	if err := os.WriteFile(tmp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the second line's actions:\n%s", out)
	}
	if css := readRuntimeCSSForTest(t); !strings.Contains(css, ".ui-row-line-2 {") || !strings.Contains(css, ".ui-row-line-actions {") {
		t.Error("the second line's actions have no layout")
	}
}

// On a phone a table row's buttons are one line; where they do not fit, the
// last ones fold into one More menu, a group's actions flattened into it, so
// none is cut off at the edge. A wide screen shows them all again, and a row
// that fits is left as it was.
func TestARowFoldsTheButtonsThatDoNotFitIntoMore(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "10_basics.js")
	start := strings.Index(src, "        var actionGroups = {}, actionGroupOrder = [];")
	endMark := "            folded = true;\n          }\n        };\n"
	end := strings.Index(src, endMark)
	if start < 0 || end < start {
		t.Fatal("the row's button layout is not found")
	}
	if !strings.Contains(src, "acts.forEach(function(a) { a.style.minWidth = ''; if (a._uiFit) a._uiFit(phone); });") {
		t.Error("rows are not fitted after a render or a resize")
	}
	harness := `
var wrapW = 0;
var actionsWrap = {kids: [], get firstChild() { return this.kids[0]; },
  removeChild: function(k) { this.kids.splice(this.kids.indexOf(k), 1); },
  get scrollWidth() { return this.kids.reduce(function(a, k) { return a + k.w; }, 0); },
  get clientWidth() { return wrapW; }};
var rec = {}, rowKey = 'name', row = {};
function appendAction(parent, act) { if (act.only_if && !rec[act.only_if]) return; parent.kids.push({label: act.label, w: 100}); }
function appendActionMenu(parent, group, items) { parent.kids.push({label: group + '[' + items.map(function(it){ return it.act.label; }).join(',') + ']', w: 90}); }
function shown() { return actionsWrap.kids.map(function(k){ return k.label; }).join(' '); }
function want(got, exp, why) { if (got !== exp) { console.log('FAIL ' + why + ': ' + got); process.exitCode = 1; } }
var cfg = {row_actions: [{label: 'Update'}, {label: 'Settings'}, {label: 'Test'},
  {label: 'Edit setup', group: 'Advanced'}, {label: 'Fix', group: 'Advanced'}, {label: 'Lead', leading: true}]};
` + src[start:end+len(endMark)] + `
want(shown(), 'Update Settings Test Advanced[Edit setup,Fix]', 'drawn whole at first');
wrapW = 1000; actionsWrap._uiFit(true);
want(shown(), 'Update Settings Test Advanced[Edit setup,Fix]', 'a phone row that fits is left as it is');
wrapW = 300; actionsWrap._uiFit(true);
want(shown(), 'Update Settings More[Test,Edit setup,Fix]', 'what does not fit folds into More, the group flattened');
wrapW = 300; actionsWrap._uiFit(false);
want(shown(), 'Update Settings Test Advanced[Edit setup,Fix]', 'a wide screen shows them all again');
wrapW = 50; actionsWrap._uiFit(true);
want(shown(), 'More[Update,Settings,Test,Edit setup,Fix]', 'at the narrowest, everything is under More');
if (!process.exitCode) console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "fold.js")
	if err := os.WriteFile(tmp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the row's buttons:\n%s", out)
	}
}
