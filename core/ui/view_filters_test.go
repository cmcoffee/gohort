package ui

// Filtering a list in the browser.
//
// Driven rather than read, for the same reason the schedule editor is: every
// way this goes wrong is quiet. A field tested the wrong way hides rows that
// should show; a count computed against the wrong set is a number nobody can
// tell is wrong by looking; a section heading left behind by its rows labels
// the wrong list.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// filterHarness slices the filter machinery out of the shipped panel and runs
// it against plain data. The functions are pure given `rows`, `filters`,
// `chosen` and `query`, so they can be exercised without a DOM.
func filterHarness(t *testing.T, body string) string {
	t.Helper()
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function optionMatches(opt, row) {")
	if i < 0 {
		t.Fatal("the filter matcher is gone")
	}
	end := strings.Index(src[i:], "function visibleRows() {")
	if end < 0 {
		t.Fatal("could not bound the matchers")
	}
	return src[i:i+end] + `
function visibleRows() {
  var q = query.trim().toLowerCase();
  return (rows || []).filter(function(row) { return passesOthers(row, -1, q); });
}
function countFor(fi, oi) {
  var q = query.trim().toLowerCase();
  var opt = (filters[fi].options || [])[oi];
  var n = 0;
  (rows || []).forEach(function(row) {
    if (passesOthers(row, fi, q) && optionMatches(opt, row)) n++;
  });
  return n;
}
function fail(m) { console.log('FAIL ' + m); process.exit(1); }
` + body + `
console.log('OK');
`
}

func runFilterJS(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	tmp := filepath.Join(t.TempDir(), "filters.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%s", out)
	}
}

// An option names a field and how to test it, and that is the whole
// vocabulary. Equals compares; a field with no Equals must merely be TRUTHY,
// which is what lets an app say "only the ones in trouble" without this
// package learning what trouble is.
func TestAChipTestsOneFieldAndNothingElse(t *testing.T) {
	runFilterJS(t, filterHarness(t, `
var rows = [
  {name: 'a', _section: 'Agents', failing: '', objective: 'ship it'},
  {name: 'b', _section: 'Agents', failing: 'failed 2 time(s) in a row'},
  {name: 'c', _section: 'Monitors', failing: ''},
];
var filters = [{options: [
  {label: 'All'},
  {label: 'Agents', field: '_section', equals: 'Agents'},
  {label: 'Failing', field: 'failing'},
  {label: 'With a goal', field: 'objective'},
]}];
var chosen = [0], query = '';
function names() { return visibleRows().map(function(r) { return r.name; }).join(','); }

if (names() !== 'a,b,c') fail('an option with no field must match everything: ' + names());
chosen = [1];
if (names() !== 'a,b') fail('equals: ' + names());
chosen = [2];
if (names() !== 'b') fail('truthy on an empty string must not match: ' + names());
chosen = [3];
if (names() !== 'a') fail('truthy on a missing field must not match: ' + names());
`))
}

// The shapes a row field actually arrives in. "0" and "false" are values the
// server chose to send, not marks of presence, and a filter that treats them as
// present shows every row on a page where nothing is wrong.
func TestFalseAndZeroAreNotPresence(t *testing.T) {
	runFilterJS(t, filterHarness(t, `
var rows = [
  {name: 'yes',   flag: true},
  {name: 'no',    flag: false},
  {name: 'zero',  flag: 0},
  {name: 'zeros', flag: '0'},
  {name: 'falses', flag: 'false'},
  {name: 'count', flag: 3},
  {name: 'absent'},
];
var filters = [{options: [{label: 'All'}, {label: 'Set', field: 'flag'}]}];
var chosen = [1], query = '';
var got = visibleRows().map(function(r) { return r.name; }).join(',');
if (got !== 'yes,count') fail('truthiness: ' + got);
`))
}

// A chip's count has to be what clicking it would LEAVE on screen, which means
// every other filter still applies. Counted in isolation it promises rows the
// click does not deliver.
func TestAChipCountsWhatClickingItWouldLeave(t *testing.T) {
	runFilterJS(t, filterHarness(t, `
var rows = [
  {name: 'a', _section: 'Agents',   failing: 'x'},
  {name: 'b', _section: 'Agents',   failing: ''},
  {name: 'c', _section: 'Monitors', failing: 'x'},
];
var filters = [
  {options: [{label: 'All'}, {label: 'Agents', field: '_section', equals: 'Agents'}]},
  {options: [{label: 'Everything'}, {label: 'Failing', field: 'failing'}]},
];
var chosen = [0, 0], query = '';
if (countFor(1, 1) !== 2) fail('unfiltered, two rows are failing: ' + countFor(1, 1));
chosen = [1, 0];
if (countFor(1, 1) !== 1) fail('narrowed to Agents, clicking Failing leaves one: ' + countFor(1, 1));
// And a chip counts itself against the OTHER filters, not against its own
// group: picking Agents must not make the Monitors count read zero-from-itself.
if (countFor(0, 0) !== 3) fail('the All chip of the active group must still count all: ' + countFor(0, 0));
`))
}

// Searching hidden fields hides and shows rows for reasons the reader cannot
// see anywhere on the page.
func TestSearchReadsOnlyWhatIsOnScreen(t *testing.T) {
	runFilterJS(t, filterHarness(t, `
var rows = [
  {name: 'nightly report', _id: 'zebra-1'},
  {name: 'zebra watch', _id: 'abc-2'},
];
var filters = [];
var chosen = [], query = 'zebra';
var got = visibleRows().map(function(r) { return r.name; }).join(',');
if (got !== 'zebra watch') fail('search matched a hidden field: ' + got);
query = 'NIGHTLY';
got = visibleRows().map(function(r) { return r.name; }).join(',');
if (got !== 'nightly report') fail('search must ignore case: ' + got);
`))
}

// The panel draws a _section heading each time the value changes from the
// previous row DRAWN, so filtering to nothing in a section must take its
// heading with it rather than leaving one over the next section's rows.
func TestAnEmptiedSectionTakesItsHeadingWithIt(t *testing.T) {
	runFilterJS(t, filterHarness(t, `
var rows = [
  {name: 'a', _section: 'Agents',   failing: ''},
  {name: 'b', _section: 'Monitors', failing: 'x'},
];
var filters = [{options: [{label: 'All'}, {label: 'Failing', field: 'failing'}]}];
var chosen = [1], query = '';
var vis = visibleRows();
var sections = [], last = null;
vis.forEach(function(r) { if (r._section !== last) { sections.push(r._section); last = r._section; } });
if (sections.join(',') !== 'Monitors') fail('headings drawn: ' + sections.join(','));
`))
}

// A live view re-fetches on a timer and re-renders through the same function.
// Without state kept outside it, a page narrowed to "the ones that need me"
// would silently widen back to everything a few seconds later, with the reader
// still looking at it.
func TestAnAutoRefreshDoesNotUndoTheFilter(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	// The timer re-renders. It must NOT go through the deliberate-open path
	// that clears the state.
	i := strings.Index(src, "if (item.auto_refresh_ms > 0) {")
	if i < 0 {
		t.Fatal("the auto-refresh timer is gone")
	}
	timer := src[i:]
	if j := strings.Index(timer, "\n          }\n"); j > 0 {
		timer = timer[:j]
	}
	if strings.Contains(timer, "clearOrchFilterState") {
		t.Error("the auto-refresh clears the filter, so a narrowed live view widens itself")
	}
	if !strings.Contains(timer, "renderOrchTable(") {
		t.Fatal("the timer no longer re-renders; this test is reading the wrong place")
	}
	// And the deliberate open must clear it, or a view comes back narrowed by a
	// choice made minutes ago, with rows missing for a reason nobody remembers.
	k := strings.Index(src, "orchView.textContent = 'Loading…';")
	if k < 0 {
		t.Fatal("the deliberate-open path has moved")
	}
	// A window rather than the whole file, so this cannot be satisfied by a
	// clear somewhere unrelated. Widened from 600 when the page_source branch
	// landed between the two: the reset is still on the deliberate-open path,
	// just further down it.
	if !strings.Contains(src[k:k+2400], "clearOrchFilterState()") {
		t.Error("opening a view does not reset its filters")
	}
}

// State from one view applied to another would highlight chips that do not
// correspond to the options on screen.
func TestFilterStateIsKeyedToItsView(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	for _, want := range []string{
		"orchFilterState.key === filterKey",
		"orchFilterState.chosen.length === chosen.length",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("restored filter state is not guarded by %q", want)
		}
	}
}

// A builder that returns ui.Section{} to mean "nothing to report" is reasonable
// to write, but the caller appends it either way. The rail labels an untitled
// section by POSITION — "Section 5" — so an empty one becomes a nav entry that
// is a bare number and opens onto nothing.
func TestAnEmptySectionIsNotRendered(t *testing.T) {
	page := Page{Sections: []Section{
		{Title: "First", Body: Card{HTML: "a"}},
		{}, // the "nothing to report" shape
		{Title: "Third", Body: Card{HTML: "c"}},
	}}
	blob, err := page.ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Sections []struct {
			Title string `json:"title"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sections) != 2 {
		t.Fatalf("%d sections rendered, want 2: the empty one became a numbered nav entry", len(cfg.Sections))
	}
	for _, s := range cfg.Sections {
		if s.Title == "" {
			t.Error("an untitled section survived")
		}
	}

	// A section with only a subtitle is NOT empty: some panels are one line of
	// prose and no title, and dropping those would be a different bug.
	page = Page{Sections: []Section{{Subtitle: "just a line"}}}
	blob, _ = page.ConfigJSON()
	_ = json.Unmarshal(blob, &cfg)
	if len(cfg.Sections) != 1 {
		t.Error("a subtitle-only section was dropped")
	}
}

// Opening a view resets its tabs; a redraw AFTER A CHANGE made inside it does
// not. Without the distinction every click bounced the reader back to the
// first tab: you set a policy on the Tools tab and landed on All, with the row
// you had just touched somewhere off screen.
func TestAChangeRedrawKeepsTheTabYouAreOn(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	// The clear is GUARDED, not removed: a deliberate open still opens whole.
	if !strings.Contains(src, "if (!keepFilters) clearOrchFilterState();") {
		t.Error("the filter reset is unguarded, so any redraw throws the tab away")
	}
	// And the reload handed to row actions asks to keep them.
	if !strings.Contains(src, "selectOrchNav(idx, extraQuery, note, true)") {
		t.Error("a post-change reload does not ask to keep the tab, so it resets")
	}
	// A plain open must NOT pass it, or a view never opens whole again.
	for _, open := range []string{"selectOrchNav(idx);", "selectOrchNav(i);"} {
		if !strings.Contains(src, open) {
			t.Errorf("the deliberate-open call %q has moved; this test reads the wrong place", open)
		}
	}
}

// The tab is a TAB, drawn the way the house draws tabs. A filter group is
// mutually exclusive with its first option as the default, which is what a tab
// bar is, and rendering the same idea two ways teaches the reader they are two
// ideas.
func TestFilterOptionsRenderAsHouseTabs(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	if !strings.Contains(src, "class: 'ui-tab'") {
		t.Error("filter options do not use the house tab class")
	}
	if !strings.Contains(src, "'ui-tab' + (chosen[ref.fi] === ref.oi ? ' active' : '')") {
		t.Error("the selected tab is not marked with the house active class")
	}
	// The multi-select picker keeps the chip styling: several can be on at
	// once there, which is what a chip is and a tab is not.
	if !strings.Contains(readRuntimeFile(t, "10_basics.js"), "'ui-chip'") {
		t.Error("the chip styling is gone from the picker, where chips are genuinely chips")
	}
}
