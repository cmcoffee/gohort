package orchestrate

// The schedule editor, driven rather than read.
//
// One frame now builds three different editors out of a pair of halves, and
// every way that goes wrong is silent: a half whose collect() is dropped sends
// a body missing the fields the user just typed, and a half that should have
// stopped the save sends one the server then has to refuse. Neither errors in
// the browser and neither shows up in a Go test of the handlers.
//
// So the shipped functions are sliced out of the asset and RUN against a stub
// DOM, exactly as they ship. The slicing is by name and brace-matching, which
// fails loudly if the file is reshaped.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// jsSpan returns the source of a top-level declaration in the app's asset,
// from the line that opens it through the brace that closes it at the same
// indent. The file indents these uniformly at six spaces.
func jsSpan(t *testing.T, src, opener string) string {
	t.Helper()
	i := strings.Index(src, opener)
	if i < 0 {
		t.Fatalf("no %q in the asset: the editor has been reshaped and this test no longer reads it", opener)
	}
	end := strings.Index(src[i:], "\n      }")
	if end < 0 {
		t.Fatalf("%q never closes at its own indent", opener)
	}
	return src[i : i+end+len("\n      }")]
}

func TestTheScheduleEditorSendsWhatWasTypedInBothHalves(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readFile(t, "assets/web_assets.html")

	var parts []string
	for _, fn := range []string{
		"function schedHalf(m, title, note) {",
		"function schedAttachHalf(m, host) {",
		"function schedArea(val, rows, placeholder) {",
		"function schedReadOnly(labelText, value) {",
		"function schedEditor(ctx, spec) {",
		"function schedLabeled(labelText, inputEl) {",
		"function schedNum(val, min) {",
		"function schedTxt(val, phold) {",
		"function schedSelect(options, selected) {",
		"function schedTime(val) {",
		"function schedButtons(m, saveBtn) {",
		"function monitorWhatFields(host, mon) {",
		"function monitorWhenFields(host, mon) {",
		"function schedForField(host, rec) {",
		"function standingWhatFields(host, sa) {",
		"function standingWhenFields(host, sa) {",
		"function schedHHMM(m) {",
		"function schedMinutes(v) {",
		"function schedDaysRow(mask) {",
		"function schedWindowRow(spec, label) {",
		"function schedTimingFields(host, spec) {",
		"function schedPeriodsFields(host, periods) {",
		"function schedUntilFields(host, until, opts) {",
	} {
		parts = append(parts, jsSpan(t, src, "      "+fn))
	}
	// The shared section's day constants are top-level vars, not functions.
	for _, v := range []string{"var SCHED_DAY_BITS = ", "var SCHED_WEEKDAYS = "} {
		i := strings.Index(src, "      "+v)
		if i < 0 {
			t.Fatalf("%q is gone from the asset", v)
		}
		parts = append(parts, src[i:i+strings.Index(src[i:], "\n")])
	}
	for _, action := range []string{"orchestrate_edit_standing", "orchestrate_edit_monitor", "orchestrate_edit_link"} {
		open := "      window.uiRegisterClientAction('" + action + "', function(ctx) {"
		i := strings.Index(src, open)
		if i < 0 {
			t.Fatalf("%s is not registered", action)
		}
		end := strings.Index(src[i:], "\n      });")
		if end < 0 {
			t.Fatalf("%s never closes", action)
		}
		parts = append(parts, src[i:i+end+len("\n      });")])
	}

	cases := []struct {
		name   string
		action string
		record string
		// edit runs after the modal is built and before Save, so a case can
		// act on the form the way a person would. global.made.textarea holds
		// every textarea the half created, in order.
		edit string
		// checks runs in node with `posted` (the parsed body or null) and
		// `alerted` (the last uiAlert message) in scope.
		checks string
	}{
		{
			name:   "a scheduled agent sends its mission and its timing together",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'review yesterday', timing: {how: 'daily', times: [540]}, schedule_label: 'every day at 09:00'}`,
			checks: `
        if (!posted) fail('nothing was sent');
        if (posted.mission !== 'review yesterday') fail('the mission half did not travel: ' + posted.mission);
        if (!posted.timing || posted.timing.how !== 'daily' || posted.timing.times[0] !== 540) fail('the timing half did not travel: ' + JSON.stringify(posted));
        if (posted.timing.days !== 0) fail('every day should send days 0: ' + posted.timing.days);
        if ('cron' in posted || 'interval_minutes' in posted) fail('the old fields were sent beside the new ones');`,
		},
		{
			name:   "a poll monitor sends its brief, its condition and its interval",
			action: "orchestrate_edit_monitor",
			record: `{name: 'build', kind: 'poll', schedulable: true, wake_brief: 'say what broke',
			          check: 'is the build red?', match_contains: 'YES', interval_seconds: 300}`,
			checks: `
        if (!posted) fail('nothing was sent');
        if (posted.wake_brief !== 'say what broke') fail('brief: ' + posted.wake_brief);
        if (posted.check !== 'is the build red?') fail('check: ' + posted.check);
        if (posted.match_contains !== 'YES') fail('match: ' + posted.match_contains);
        if (posted.interval_seconds !== 300) fail('interval: ' + posted.interval_seconds);
        if (!posted.periods || posted.periods.days !== 0 || posted.periods.has_window) fail('a monitor should stay continuous by default: ' + JSON.stringify(posted.periods));
        if ('url' in posted) fail('a poll monitor sent a url, which the server refuses by name');`,
		},
		{
			// The advanced option: same cadence, only on the days and hours set.
			name:   "a monitor can be kept to set time periods",
			action: "orchestrate_edit_monitor",
			record: `{name: 'build', kind: 'poll', schedulable: true, wake_brief: 'say what broke',
			          check: 'is the build red?', match_contains: 'YES', interval_seconds: 30}`,
			edit: `global.made.select.filter(function(x) { return x.children.some(function(o) { return o.value === 'periods'; }); })[0].value = 'periods';
        var cbs = global.made.input.filter(function(x) { return x.type === 'checkbox'; });
        cbs[5].checked = false; cbs[6].checked = false;
        var tms = global.made.input.filter(function(x) { return x.type === 'time'; });
        tms[tms.length - 2].value = '09:00'; tms[tms.length - 1].value = '17:00';`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        if (posted.interval_seconds !== 30) fail('the cadence moved: ' + posted.interval_seconds);
        var p = posted.periods || {};
        if (p.days !== 62 || !p.has_window || p.window_from !== 540 || p.window_to !== 1020) fail('periods: ' + JSON.stringify(p));`,
		},
		{
			// The reason the editor is offered on every kind: a webhook has no
			// clock, and its timing half must send nothing rather than a zero.
			name:   "a push-triggered monitor sends a brief and no interval at all",
			action: "orchestrate_edit_monitor",
			record: `{name: 'inbound', kind: 'webhook', schedulable: false, wake_brief: 'something arrived'}`,
			checks: `
        if (!posted) fail('nothing was sent');
        if (posted.wake_brief !== 'something arrived') fail('brief: ' + posted.wake_brief);
        if ('interval_seconds' in posted) fail('a webhook sent an interval, which the server refuses');
        if ('check' in posted) fail('a webhook sent a condition it does not have');`,
		},
		{
			// A half that cannot validate returns null, and the save must stop
			// in the browser rather than posting a body the server refuses.
			name:   "emptying a mission it had stops the save before it leaves",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'review yesterday', timing: {how: 'daily', times: [540]}}`,
			edit:   `global.made.textarea[0].value = '   ';`,
			checks: `
        if (posted) fail('an emptied mission was posted anyway: ' + JSON.stringify(posted));
        if (!alerted || alerted.indexOf('mission') < 0) fail('nothing told the user why: ' + alerted);`,
		},
		{
			// The mirror of it, and the reason the rule is about BLANKING
			// rather than about having one: a schedule driving a pipeline or a
			// machine may carry no mission at all, and it must stay editable.
			name:   "a schedule that never had a mission still saves its timing",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: '', targets_run: true, runs: 'pipeline - nightly', timing: {how: 'daily', times: [540]}}`,
			checks: `
        if (!posted) fail('a missionless pipeline schedule could not be retimed: ' + alerted);
        if (!posted.timing || posted.timing.times[0] !== 540) fail('timing: ' + JSON.stringify(posted));`,
		},
		{
			// Same for a monitor created without a brief, which is what
			// create_event_monitor produces when none is given.
			name:   "a monitor that never had a brief still saves its condition",
			action: "orchestrate_edit_monitor",
			record: `{name: 'feed', kind: 'watch', schedulable: true, wake_brief: '',
			          tool_name: 'bridge_cred_x', format_script: '', interval_seconds: 3600}`,
			checks: `
        if (!posted) fail('a briefless monitor could not be saved: ' + alerted);
        if (posted.wake_brief !== '') fail('brief: ' + JSON.stringify(posted.wake_brief));
        if (posted.interval_seconds !== 3600) fail('interval: ' + posted.interval_seconds);`,
		},
		{
			// Observed: a scheduled agent set to "Every N minutes" could not be
			// moved to a random cadence, because the editor had no such choice.
			name:   "an every-N schedule can be switched to random gaps",
			action: "orchestrate_edit_standing",
			record: `{name: 'poster', mission: 'post something', timing: {how: 'every', every_seconds: 2700}}`,
			edit: `global.made.select[1].value = 'random_gap';
        global.made.select[3].value = 'minutes';
        global.made.input[12].value = '30';
        global.made.input[13].value = '60';`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (tm.how !== 'random_gap' || tm.min_gap_seconds !== 1800 || tm.max_gap_seconds !== 3600) fail('gaps: ' + JSON.stringify(tm));`,
		},
		{
			name:   "a random schedule can be kept to a window of the day",
			action: "orchestrate_edit_standing",
			record: `{name: 'poster', mission: 'post something', timing: {how: 'random_gap', min_gap_seconds: 1800, max_gap_seconds: 3600}}`,
			edit: `global.made.input[14].value = '09:00';
        global.made.input[15].value = '17:00';`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (!tm.has_window || tm.window_from !== 540 || tm.window_to !== 1020) fail('window: ' + JSON.stringify(tm));`,
		},
		{
			name:   "half a window is refused before it is sent",
			action: "orchestrate_edit_standing",
			record: `{name: 'poster', mission: 'post something', timing: {how: 'random_gap', min_gap_seconds: 1800, max_gap_seconds: 3600}}`,
			edit:   `global.made.input[14].value = '09:00';`,
			checks: `
        if (posted) fail('half a window was posted: ' + JSON.stringify(posted));
        if (!alerted || alerted.indexOf('both ends') < 0) fail('nothing said why: ' + alerted);`,
		},
		{
			// An annual date ("DEC-25 09:00") reaches the dialog as once a
			// year; it used to open as a default daily time, and saving it
			// turned the yearly schedule into a daily one.
			name:   "a yearly schedule opens as yearly and keeps its date",
			action: "orchestrate_edit_standing",
			record: `{name: 'greeting', mission: 'send greetings', timing: {how: 'yearly', month: 12, day: 25, times: [540]}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (tm.how !== 'yearly' || tm.month !== 12 || tm.day !== 25 || !tm.times || tm.times[0] !== 540) fail('a re-save lost the date: ' + JSON.stringify(tm));`,
		},
		{
			name:   "a yearly date no year has is refused",
			action: "orchestrate_edit_standing",
			record: `{name: 'greeting', mission: 'send greetings', timing: {how: 'yearly', month: 2, day: 30, times: [540]}}`,
			checks: `
        if (posted) fail('Feb 30 was posted: ' + JSON.stringify(posted));
        if (!alerted || alerted.indexOf('February has no day 30') < 0) fail('nothing said why: ' + alerted);`,
		},
		{
			// Once a month counted from the end ("the last day") opens as
			// once a month and keeps its day, rather than turning daily.
			name:   "a monthly schedule opens as monthly and keeps its day",
			action: "orchestrate_edit_standing",
			record: `{name: 'roundup', mission: 'post the releases', timing: {how: 'monthly', day: -1, times: [540]}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (tm.how !== 'monthly' || tm.day !== -1 || !tm.times || tm.times[0] !== 540) fail('a re-save lost the day: ' + JSON.stringify(tm));`,
		},
		{
			name:   "a daily schedule can be made monthly",
			action: "orchestrate_edit_standing",
			record: `{name: 'roundup', mission: 'post the releases', timing: {how: 'daily', times: [540]}}`,
			edit: `global.made.select[1].value = 'monthly';
        global.made.select[5].value = '28';`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (tm.how !== 'monthly' || tm.day !== 28 || !tm.times || tm.times[0] !== 540 || tm.days) fail('monthly: ' + JSON.stringify(tm));`,
		},
		{
			name:   "a random schedule opens as random and keeps its gaps",
			action: "orchestrate_edit_standing",
			record: `{name: 'poster', mission: 'post something', timing: {how: 'random_gap', min_gap_seconds: 1800, max_gap_seconds: 3600}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var tm = posted.timing || {};
        if (tm.how !== 'random_gap' || tm.min_gap_seconds !== 1800 || tm.max_gap_seconds !== 3600) fail('a re-save flattened the random gaps: ' + JSON.stringify(tm));`,
		},
		{
			name:   "a random schedule whose longest gap is not longer is refused",
			action: "orchestrate_edit_standing",
			record: `{name: 'poster', mission: 'post something', timing: {how: 'random_gap', min_gap_seconds: 1800, max_gap_seconds: 3600}}`,
			edit:   `global.made.input[13].value = '20';`,
			checks: `
        if (posted) fail('an inverted range was posted: ' + JSON.stringify(posted));
        if (!alerted || alerted.indexOf('longest gap') < 0) fail('nothing said why: ' + alerted);`,
		},
		{
			// Days are always shown as boxes; unticking the weekend is weekdays.
			name:   "days are ticked boxes, sent as a mask",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}}`,
			edit:   `global.made.input[5].checked = false; global.made.input[6].checked = false;`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        if (posted.timing.days !== 62) fail('weekdays mask: ' + posted.timing.days);`,
		},
		{
			name:   "no days ticked is refused",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}}`,
			edit:   `for (var i = 0; i < 7; i++) global.made.input[i].checked = false;`,
			checks: `
        if (posted) fail('a schedule with no days was posted');
        if (!alerted || alerted.indexOf('at least one day') < 0) fail('nothing said why: ' + alerted);`,
		},
		{
			// Until: nothing set is indefinitely, and that is what is sent.
			name:   "a schedule with no stops runs indefinitely",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var u = posted.until || {};
        if (u.stop_on !== '' || u.max_runs !== 0 || u.goal !== '') fail('indefinitely: ' + JSON.stringify(u));`,
		},
		{
			name:   "a date and a number of runs are sent together",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}, until: {stop_on: '2026-11-30', max_runs: 10, has_goal: true}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var u = posted.until || {};
        if (u.stop_on !== '2026-11-30' || u.max_runs !== 10) fail('stops: ' + JSON.stringify(u));`,
		},
		{
			name:   "a goal is sent with its tries",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}, until: {goal: 'the PR is merged', max_attempts: 5, has_goal: true}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        var u = posted.until || {};
        if (u.goal !== 'the PR is merged' || u.max_attempts !== 5) fail('goal: ' + JSON.stringify(u));`,
		},
		{
			name:   "a ticked count with no number is refused",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}}`,
			edit: `global.made.input.filter(function(x) { return x.type === 'radio' && x.value === 'until'; })[0].checked = true;
        var cbs = global.made.input.filter(function(x) { return x.type === 'checkbox'; });
        cbs[8].checked = true;`,
			checks: `
        if (posted) fail('a count with no number was posted');
        if (!alerted || alerted.indexOf('how many') < 0) fail('nothing said why: ' + alerted);`,
		},
		{
			// A link group stops on a date or after rounds; it has no goal.
			name:   "a link group sends its time and its stops, and no goal",
			action: "orchestrate_edit_link",
			record: `{members: ['1. gather', '2. post'], timing: {how: 'random_gap', min_gap_seconds: 1800, max_gap_seconds: 3600}, until: {max_runs: 4, has_goal: false}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        if (!posted.timing || posted.timing.how !== 'random_gap') fail('timing: ' + JSON.stringify(posted));
        var u = posted.until || {};
        if (u.max_runs !== 4 || 'goal' in u) fail('group stops: ' + JSON.stringify(u));`,
		},
		{
			name:   "a monitor stops after a number of alerts",
			action: "orchestrate_edit_monitor",
			record: `{name: 'build', kind: 'poll', schedulable: true, wake_brief: 'b', check: 'c?', match_contains: 'YES', interval_seconds: 30,
			          until: {max_runs: 3, has_goal: true}}`,
			checks: `
        if (!posted) fail('nothing was sent: ' + alerted);
        if ((posted.until || {}).max_runs !== 3) fail('alerts: ' + JSON.stringify(posted.until));`,
		},
		{
			// Until is one choice first; picking it with nothing under it says so.
			name:   "Until with no condition picked is refused",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'x', timing: {how: 'daily', times: [540]}}`,
			edit:   `global.made.input.filter(function(x) { return x.type === 'radio' && x.value === 'until'; })[0].checked = true;`,
			checks: `
        if (posted) fail('an empty Until was posted');
        if (!alerted || alerted.indexOf('Indefinitely') < 0) fail('nothing said why: ' + alerted);`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			harness := `
function fail(msg) { console.log('FAIL ' + msg); process.exit(1); }
function node(tag) {
  var n = {
    tagName: tag, value: '', textContent: '', className: '', disabled: false, type: '',
    style: {cssText: ''}, children: [], options: [],
    // A real <select> reports the value of whichever <option> is selected.
    // Without that the unit picker reads as empty and an interval of "5
    // minutes" is collected as five seconds, which is the harness lying, not
    // the editor.
    appendChild: function(c) {
      this.children.push(c);
      if (c && c.selected && this.value === '') this.value = c.value;
      return c;
    },
    setAttribute: function() {}, addEventListener: function() {},
    map: undefined,
    get firstChild() { return this.children[0] || null; },
  };
  (global.made = global.made || {});
  (global.made[tag] = global.made[tag] || []).push(n);
  return n;
}
global.document = {createElement: node, createTextNode: function(t) { return {text: t}; }};
var alerted = null;
var posted = null;
global.window = {
  uiAlert: function(m) { alerted = String(m); },
  uiRegisterClientAction: function(name, fn) { (global.actions = global.actions || {})[name] = fn; },
};
function openOrchModal(title) {
  return {body: node('div'), footer: node('div'), close: function() { global.closed = true; }};
}
global.fetch = function(url, opts) {
  if (!opts || !opts.method) {
    return Promise.resolve({ok: true, json: function() { return Promise.resolve(` + c.record + `); }});
  }
  posted = JSON.parse(opts.body);
  return Promise.resolve({ok: true, text: function() { return Promise.resolve(''); }});
};

` + strings.Join(parts, "\n\n") + `

var fn = global.actions['` + c.action + `'];
if (typeof fn !== 'function') fail('the action did not register');
fn({id: 'x', reload: function() {}});

setTimeout(function() {
  // The Save button is the primary one the frame appends last.
  var save = null;
  if (!global.savedButton) fail('no modal was built');
  save = global.savedButton;
` + c.edit + `
  save.onclick();
  setTimeout(function() {
` + c.checks + `
    console.log('OK');
  }, 5);
}, 5);
`
			// The frame hands its Save button to schedButtons; capture it there
			// rather than guessing at the footer's shape.
			harness = strings.Replace(harness,
				"function schedButtons(m, saveBtn) {",
				"function schedButtons(m, saveBtn) {\n        global.savedButton = saveBtn;", 1)
			if !strings.Contains(harness, "global.savedButton = saveBtn;") {
				t.Fatal("schedButtons is no longer where the frame hands over its Save button")
			}

			tmp := filepath.Join(t.TempDir(), "sched.js")
			if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("node", tmp).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "OK") {
				t.Fatalf("%s\n%s", fmt.Sprint(err), out)
			}
		})
	}
}

// The machine-run creator sends the shared timing and Until like every other
// dialog. Driven: the machine list loads, the form is filled in, Create sends.
func TestTheMachineRunCreatorSendsTimingAndUntil(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readFile(t, "assets/web_assets.html")
	var parts []string
	for _, fn := range []string{
		"function schedLabeled(labelText, inputEl) {", "function schedNum(val, min) {", "function schedTxt(val, phold) {",
		"function schedSelect(options, selected) {", "function schedTime(val) {", "function schedHHMM(m) {",
		"function schedMinutes(v) {", "function schedDaysRow(mask) {", "function schedWindowRow(spec, label) {",
		"function schedTimingFields(host, spec) {", "function schedUntilFields(host, until, opts) {",
	} {
		parts = append(parts, jsSpan(t, src, "      "+fn))
	}
	for _, v := range []string{"var SCHED_DAY_BITS = ", "var SCHED_WEEKDAYS = "} {
		i := strings.Index(src, "      "+v)
		parts = append(parts, src[i:i+strings.Index(src[i:], "\n")])
	}
	open := "      window.uiRegisterClientAction('orchestrate_new_machine_run', function(ctx) {"
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatal("the machine-run creator is gone")
	}
	parts = append(parts, src[i:i+strings.Index(src[i:], "\n      });")+len("\n      });")])

	harness := `
function fail(m) { console.log('FAIL ' + m); process.exit(1); }
function node(tag) {
  var n = {tagName: tag, value: '', textContent: '', className: '', disabled: false, type: '', checked: false,
    style: {cssText: ''}, children: [], options: [],
    appendChild: function(c) { this.children.push(c); if (c && c.selected && this.value === '') this.value = c.value; return c; },
    setAttribute: function() {}, addEventListener: function() {}};
  (global.made = global.made || {});
  (global.made[tag] = global.made[tag] || []).push(n);
  return n;
}
global.document = {createElement: node, createTextNode: function(t) { return {text: t}; }};
var alerted = null, posted = null, modal = null;
global.window = {
  uiAlert: function(m) { alerted = String(m); },
  uiRegisterClientAction: function(name, fn) { (global.actions = global.actions || {})[name] = fn; },
  GOHORT_AGENT_ID: 'agent-1',
};
function openOrchModal(title) { modal = {body: node('div'), footer: node('div'), close: function() {}}; return modal; }
global.fetch = function(url, opts) {
  if (!opts || !opts.method) return Promise.resolve({ok: true, json: function() { return Promise.resolve([{value: 'm1', label: 'Digest'}]); }});
  posted = JSON.parse(opts.body);
  return Promise.resolve({ok: true, text: function() { return Promise.resolve(''); }});
};
` + strings.Join(parts, "\n\n") + `
global.actions['orchestrate_new_machine_run']({reload: function() {}});
setTimeout(function() {
  var create = modal.footer.children[modal.footer.children.length - 1];
  global.made.input[0].value = 'nightly digest';
  global.made.select.filter(function(x) { return x.children.some(function(o) { return o.value === 'm1'; }); })[0].value = 'm1';
  var cbs = global.made.input.filter(function(x) { return x.type === 'checkbox'; });
  global.made.input.filter(function(x) { return x.type === 'radio' && x.value === 'until'; })[0].checked = true;
  cbs[8].checked = true; // It has run a number of times
  var nums = global.made.input.filter(function(x) { return x.type === 'number'; });
  nums[nums.length - 2].value = '5'; // the runs field, before the goal's tries
  create.onclick();
  setTimeout(function() {
    if (!posted) fail('nothing was sent: ' + alerted);
    if (posted.machine_id !== 'm1' || !posted.timing || posted.timing.how !== 'daily') fail('timing: ' + JSON.stringify(posted));
    if (!posted.until || posted.until.max_runs !== 5) fail('until: ' + JSON.stringify(posted.until));
    console.log('OK');
  }, 5);
}, 5);
`
	tmp := filepath.Join(t.TempDir(), "machine.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%v\n%s", err, out)
	}
}
