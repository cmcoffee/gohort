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
		"function standingWhatFields(host, sa) {",
		"function standingWhenFields(host, sa) {",
	} {
		parts = append(parts, jsSpan(t, src, "      "+fn))
	}
	for _, action := range []string{"orchestrate_edit_standing", "orchestrate_edit_monitor"} {
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
		// checks runs in node with `posted` (the parsed body or null) and
		// `alerted` (the last uiAlert message) in scope.
		checks string
	}{
		{
			name:   "a scheduled agent sends its mission and its timing together",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: 'review yesterday', cron: 'daily 09:00', schedule_label: 'every day at 09:00'}`,
			checks: `
        if (!posted) fail('nothing was sent');
        if (posted.mission !== 'review yesterday') fail('the mission half did not travel: ' + posted.mission);
        if (posted.cron !== 'daily 09:00') fail('the timing half did not travel: ' + JSON.stringify(posted));
        if (posted.interval_minutes !== 0) fail('a cron schedule sent an interval too, so the record disagrees with itself');`,
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
        if ('url' in posted) fail('a poll monitor sent a url, which the server refuses by name');`,
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
			name:   "an emptied mission stops the save before it leaves",
			action: "orchestrate_edit_standing",
			record: `{name: 'nightly', mission: '   ', cron: 'daily 09:00'}`,
			checks: `
        if (posted) fail('an empty mission was posted anyway: ' + JSON.stringify(posted));
        if (!alerted || alerted.indexOf('mission') < 0) fail('nothing told the user why: ' + alerted);`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			harness := `
function fail(msg) { console.log('FAIL ' + msg); process.exit(1); }
function node(tag) {
  return {
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
  };
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
