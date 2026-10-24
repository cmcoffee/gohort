package ui

// A message that was shown as a card (an ask with choices, a form) is drawn as
// that card again when the thread is redrawn, rather than as the placeholder
// text stored beside it, and an answered one has its controls switched off.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAStoredCardIsRedrawnAsACard(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	fn := boundedRuntime(t, src, "function replayBlock(", "\n    }\n")
	if !strings.Contains(src, "var drawn = replayBlock(m.block, answered, answer);") {
		t.Fatal("the thread redraw no longer draws stored cards")
	}
	harness := `
var activeSessionId = 's1';
function control(keep) { return {disabled: false, classList: {contains: function(c) { return !!keep && c === 'ui-ask-keep'; }}}; }
function card() {
  var controls = [control(), control(), control(true)];
  return {classes: [], controls: controls,
    classList: {add: function(c) { this.owner.classes.push(c); }},
    querySelectorAll: function() { return controls; }};
}
var drawn = [];
var convoLog = {appendChild: function(n) { drawn.push(n); }};
var window = {UIBlockRenderers: {ui_ask_form: function(d, ctx) {
  var c = card(); c.classList.owner = c; c.steps = d.steps; c.ctx = ctx; return {wrap: c};
}}};
` + fn + `
if (!replayBlock({type: 'ui_ask_form', steps: [1, 2, 3]}, false)) throw new Error('a form was not redrawn');
var open = drawn[0];
if (open.steps.length !== 3 || !open.ctx.replayed) throw new Error('drawn without its steps');
if (open.controls[0].disabled || open.classes.length) throw new Error('an unanswered form was switched off');

var answer = {values: ['x']};
if (replayBlock({type: 'ui_ask_form', steps: [1]}, true, answer) !== drawn[1]) throw new Error('the drawn card is not returned');
var done = drawn[1];
if (!done.controls[0].disabled || !done.controls[1].disabled || done.classes.indexOf('submitted') < 0) throw new Error('an answered form is still live');
if (done.controls[2].disabled) throw new Error('Change answer was switched off with the rest');
if (done.ctx.answer !== answer) throw new Error('the answer state did not reach the card');

if (replayBlock({type: 'nobody_renders_this'}, false)) throw new Error('an unknown card claimed to be drawn');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "replay.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("stored cards:\n%s", out)
	}
}
