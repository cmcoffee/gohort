package ui

// One dropdown per Menu name. The Group heading that preceded it put every
// entry behind a button called something else, so a fleet-wide pane still
// opened from a menu sitting in one agent's topbar — the heading said "Your
// fleet" and the button said "Manage", and the button is what you read first.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavMenuMarshals(t *testing.T) {
	raw, _ := json.Marshal(OrchestratorNavItem{Label: "Overview", Menu: "Fleet", Scope: "fleet"})
	if !strings.Contains(string(raw), `"menu":"Fleet"`) {
		t.Errorf("Menu did not survive marshalling:\n%s", raw)
	}
	// Unset stays absent, so an app that declares one flat menu is unchanged.
	plain, _ := json.Marshal(OrchestratorNavItem{Label: "Runs", Source: "api/runs"})
	if strings.Contains(string(plain), "menu") {
		t.Errorf("menu should be omitted when unset: %s", plain)
	}
}

// The naming rule, run rather than read: menus are created on first mention,
// in that order, labelled with the name itself, and an item that names none
// falls into a single default rather than one menu each.
func TestMenusAreBuiltOnFirstMentionInOrder(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "var DEFAULT_NAV_MENU =")
	if i < 0 {
		t.Fatal("the menu factory is gone")
	}
	const tail = "function closeNavMenus()"
	end := strings.Index(src[i:], tail)
	if end < 0 {
		t.Fatal("could not bound the menu factory")
	}
	factory := src[i : i+end]

	harness := `
var navMenus = [], navMenuByName = {};
var appended = [];
function el(tag, attrs, kids) {
  return {tag: tag, attrs: attrs || {}, kids: kids || [], style: {},
          classList: {add: function(){}, remove: function(){}},
          appendChild: function(c) { this.kids.push(c); },
          contains: function() { return false; }};
}
global.document = {addEventListener: function() {}};
function clearOpenTopbarMenu() {}
function setOpenTopbarMenu() {}
function refreshChannelBadges() {}
` + factory + `
// Declaration order is the topbar order, and a repeat does not open a second.
[{menu: 'Agent'}, {menu: 'Cortex'}, {menu: 'Fleet'}, {menu: 'Agent'}, {}]
  .forEach(function(it) { navMenuFor(it); });

var names = navMenus.map(function(m) { return m.name; }).join(',');
if (names !== 'Agent,Cortex,Fleet,Manage') throw new Error('menu order/identity wrong: ' + names);

// The button says the menu's own name — nothing here knows what it is called.
var labels = navMenus.map(function(m) { return m.btn.kids[0]; }).join('|');
if (labels !== 'Agent ▾|Cortex ▾|Fleet ▾|Manage ▾') throw new Error('button labels wrong: ' + labels);

// Each panel is its own element, so one open menu cannot show another's items.
if (navMenus[0].panel === navMenus[1].panel) throw new Error('two menus share one panel');

// Closing is per menu and leaves the rest alone.
navMenus[0].panel.style.display = 'flex';
navMenus[1].panel.style.display = 'flex';
navMenus[0].close();
if (navMenus[0].panel.style.display !== 'none') throw new Error('close did not hide its own panel');
if (navMenus[1].panel.style.display !== 'flex') throw new Error('closing one menu closed another');

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "menus.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the menu factory does not hold:\n%s", out)
	}
}

// A summary figure reaching the rows it counts. The target is resolved by
// menu AND label because a Source appears in two menus, and the query rides
// the open without changing what the view's own button asks.
func TestViewActionResolvesItsTarget(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "if (a.view) {")
	if i < 0 {
		t.Fatal("the navigating row action is gone")
	}
	// Bounded on the next statement by NAME, not by its indentation: the
	// panel's row rendering has been nested a level deeper once already, and a
	// slice pinned to a column count fails on a change that moved no code.
	end := strings.Index(src[i:], "var rowURL")
	if end < 0 {
		t.Fatal("could not bound the view branch")
	}
	branch := src[i : i+end]

	harness := `
var DEFAULT_NAV_MENU = 'Manage';
var cfg = {orchestrator_nav: [
  {menu: 'Manage', label: 'Overview'},
  {menu: 'Manage', label: 'Runs'},
  {menu: 'Fleet',  label: 'Overview'},
  {menu: 'Fleet',  label: 'Runs'},
  {label: 'Leftovers'},
]};
global.window = {GOHORT_AGENT_ID: 'agent-7'};
var closed = 0, picked = null;
function closeNavMenus() { closed++; }
function selectOrchNav(i, q) { picked = {i: i, q: q}; }
function go(a) { ` + branch + `
}
// Two menus hold a view called Runs; the target names which.
go({view: 'Fleet/Runs', query: 'status=failed'});
if (!picked || picked.i !== 3) throw new Error('resolved to the wrong Runs: ' + JSON.stringify(picked));
if (picked.q !== 'status=failed') throw new Error('query lost: ' + picked.q);
if (!closed) throw new Error('the menu stayed open behind the view it opened');

// {agent} lets a per-agent figure hand its scope to a fleet-wide view.
go({view: 'Manage/Runs', query: 'status=failed&agent={agent}'});
if (picked.i !== 1) throw new Error('menu was ignored: ' + picked.i);
if (picked.q !== 'status=failed&agent=agent-7') throw new Error('agent not substituted: ' + picked.q);

// An item that named no menu is reachable under the default.
go({view: 'Manage/Leftovers'});
if (picked.i !== 4) throw new Error('default-menu item unreachable: ' + picked.i);

// A target that does not exist navigates nowhere rather than somewhere wrong.
picked = null;
console.error = function() {};
go({view: 'Fleet/Nope', query: 'x=1'});
if (picked !== null) throw new Error('an unresolved target still navigated: ' + JSON.stringify(picked));

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "view.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the view action does not resolve correctly:\n%s", out)
	}
}

// A group heading has to read as a division, not as one more row. In a column
// of full-width buttons a bare line of text at the same width is a button that
// happens not to respond, so the heading carries a rule — except when it opens
// the menu and has nothing to divide from.
func TestGroupHeadingDrawsARule(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "if (item.group && item.group !== menu.lastGroup) {")
	if i < 0 {
		t.Fatal("the group heading is gone")
	}
	block := src[i : i+900]
	if !strings.Contains(block, "firstInMenu") {
		t.Error("the heading does not distinguish the first group in a menu, so it draws a rule against nothing")
	}
	if !strings.Contains(block, "border-top") {
		t.Error("the heading draws no rule, so it renders as another row in the list")
	}
}

// A narrowed view has to say so and has to offer the way out. Without both, a
// filtered pane is the whole pane with fewer rows: the reader either reads a
// partial list as complete, or cannot get back to the rest.
func TestNarrowedViewIsMarkedAndReversible(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function paintNarrowNote(")
	if i < 0 {
		t.Fatal("the narrowed-view marker is gone")
	}
	end := strings.Index(src[i:], "function selectOrchNav(")
	if end < 0 {
		t.Fatal("could not bound paintNarrowNote")
	}
	fn := src[i : i+end]

	harness := `
function el(tag, attrs, kids) {
  var n = {tag: tag, attrs: attrs || {}, kids: kids || [], style: {},
    appendChild: function(c) { this.kids.push(c); return c; },
    insertBefore: function(c) { this.kids.unshift(c); return c; }};
  Object.defineProperty(n, 'firstChild', {get: function() { return this.kids[0] || null; }});
  return n;
}
function text(n) {
  if (n == null) return '';
  if (typeof n === 'string') return n;
  return (n.kids || []).map(text).join(' ');
}
var orchView = null, picked = 'none';
function selectOrchNav(i, q, note) { picked = {i: i, q: q, note: note}; }
` + fn + `

// An unnarrowed open paints nothing — the ordinary case must stay unmarked.
orchView = el('div');
paintNarrowNote(3, {}, '', '');
if (orchView.kids.length !== 0) throw new Error('an unfiltered view was marked');

// The app's wording wins, and the way back sits beside it.
orchView = el('div');
orchView.appendChild(el('div', {}, ['row one']));
paintNarrowNote(3, {}, 'status=failed', 'Showing failed runs');
var bar = orchView.kids[0];
if (text(bar).indexOf('Showing failed runs') < 0) throw new Error('note missing: ' + text(bar));
if (text(bar).indexOf('Show all') < 0) throw new Error('no way back: ' + text(bar));
if (text(orchView.kids[1]) !== 'row one') throw new Error('the marker displaced the rows');

// Show all returns to the same view asking its own question.
bar.kids[1].attrs.onclick();
if (picked.i !== 3 || picked.q !== undefined) throw new Error('Show all did not clear the narrowing: ' + JSON.stringify(picked));

// No wording from the app still marks the pane, from the query itself — and
// the agent stamp is scope, not a filter anyone chose, so it is not listed.
orchView = el('div');
paintNarrowNote(1, {}, 'status=failed&agent=agent-7', '');
var auto = text(orchView.kids[0]);
if (auto.indexOf('status: failed') < 0) throw new Error('query not rendered: ' + auto);
if (auto.indexOf('agent') >= 0) throw new Error('the agent stamp was listed as a filter: ' + auto);

// A narrowing that matched NOTHING is when the way back matters most, and the
// render returns early there, so the marker has to survive an empty pane.
orchView = el('div');
orchView.appendChild(el('div', {}, ['Nothing here yet.']));
paintNarrowNote(1, {}, 'status=failed', 'Showing failed runs');
if (text(orchView.kids[0]).indexOf('Show all') < 0) throw new Error('an empty filtered pane has no way back');

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "narrow.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the narrowed-view marker does not hold:\n%s", out)
	}
}
