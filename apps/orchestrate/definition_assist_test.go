package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The ✨ Get assistance dialog on a machine's or a pipeline's editor, run:
// its real script under node, over a stand-in page and conversation pane.
// An answer drafts nothing; a change is drafted (moving, and stoppable),
// shown as a diff per part, and applied with the fingerprint it was drafted
// against; a finding opens it with its request already sent. Skips where
// node is not installed or too old for AbortController.
func TestTheDefinitionDialogDraftsShowsAndApplies(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "-p", "process.versions.node.split('.')[0]").Output(); err == nil {
		if major, _ := strconv.Atoi(strings.TrimSpace(string(out))); major < 16 {
			t.Skipf("node %s has no AbortController; CI runs 20", strings.TrimSpace(string(out)))
		}
	}
	harness := `
function node(tag) {
  var n = {tag: tag, kids: [], attrs: {}, style: {}, textContent: '', className: '',
    set innerHTML(v) { this.kids = []; this.html = v; },
    appendChild: function(c) { this.kids.push(c); return c; },
    insertBefore: function(c) { this.kids.unshift(c); return c; },
    setAttribute: function(k, v) { this.attrs[k] = v; }};
  return n;
}
function text(n) { return (n.textContent || '') + n.kids.map(text).join(' '); }
var slot = node('span');
var document = {createElement: node, getElementById: function() { return null; },
  querySelector: function(sel) { return sel === '.ui-section .ui-section-h-r' ? slot : null; }};
function requestAnimationFrame() {}
var modal = null, chatOpts = null, said = [], calls = [], reloaded = false;
var location = {reload: function() { reloaded = true; }};
function fetch(url, o) {
  return new Promise(function(res, rej) {
    var c = {url: url, body: JSON.parse(o.body), res: res};
    calls.push(c);
    if (o.signal) o.signal.addEventListener('abort', function() { var e = new Error('aborted'); e.name = 'AbortError'; rej(e); });
  });
}
function answer(v) { return {ok: true, json: function() { return Promise.resolve(v); }}; }
var window = {
  uiOpenModal: function(o) { modal = o; },
  uiAssistChat: function(o) { chatOpts = o; return {right: node('div'), focus: function() {}, say: function(t) { said.push(t); }}; },
  uiAssistPost: function(url) { return url; },
  uiAlert: function() {},
  editorDiffRender: function(a, b) { return 'DIFF[' + a + '|' + b + ']'; },
};
` + definitionAssistJS + "\n" + machineAssist(MachineDef{ID: "m1"}).js() + `
function sleep(ms) { return new Promise(function(r) { setTimeout(r, ms); }); }
var fail = 0;
function check(label, cond) { if (!cond) { fail++; console.log('FAIL ' + label); } }
(async function() {
  await sleep(80);
  var b = slot.kids[0];
  check('the button is in the header, labelled', b && b.textContent === '✨ Get assistance');
  b.onclick();
  var body = node('div');
  modal.mount(body, {close: function() {}});
  var review = body.kids[1], apply = body.kids[2].kids[0];
  check('the conversation is the machine\'s', chatOpts.send === 'api/machines/m1/assist');
  check('nothing proposed yet: no Apply', apply.style.display === 'none');
  check('an answer drafts nothing', chatOpts.onResult({reply: 'It has three steps.'}) === '' && calls.length === 0);

  chatOpts.onResult({reply: 'Adding a dig step.', change: 'Add a dig step after sort.'});
  check('a change is drafted from what was agreed', calls.length === 1 && calls[0].url === 'api/machines/m1/propose' &&
    calls[0].body.description === 'Add a dig step after sort.');
  check('and the draft shows it is working', /Drafting the change/.test(text(review)) && /Stop/.test(text(review)));
  calls[0].res(answer({proposal: {name: 'Triage', phases: [{name: 'sort'}]}, from: 'f1', changed: ['added dig'],
    changes: [{label: 'New step "dig"', current: '', value: 'look at the logs'}], checklist: ['step dig: no way out']}));
  await sleep(20);
  var shown = text(review);
  check('the proposal is shown: what was asked, the summary, each part as a diff, what is still to fix',
    /Proposed: Add a dig step after sort\./.test(shown) && /added dig/.test(shown) &&
    JSON.stringify(review).indexOf('DIFF[|look at the logs]') >= 0 && /Still to fix/.test(shown));
  check('and only now can it be applied', apply.style.display === '');
  apply.onclick();
  check('Apply sends the proposal and what it was drafted against', calls.length === 2 && calls[1].url === 'api/machines/m1/revise' &&
    calls[1].body.from === 'f1' && calls[1].body.proposal.name === 'Triage');
  calls[1].res({ok: true});
  await sleep(10);
  check('and reloads to show what was stored', reloaded);

  // A draft can be stopped, and saying so leaves nothing changed.
  chatOpts.onResult({change: 'Something slow.'});
  var stop = review.kids[0].kids[1];
  stop.onclick();
  await sleep(10);
  check('a stopped draft says nothing was changed', /Stopped\. Nothing was changed\./.test(text(review)) && apply.style.display === 'none');

  // A finding opens the dialog with its request already sent.
  window.orchDefinitionAssistOpen('Rewrite the instructions of the step "sort".');
  var again = node('div');
  modal.mount(again, {close: function() {}});
  check('a finding opens it with its request sent', said[said.length - 1] === 'Rewrite the instructions of the step "sort".');
  if (fail) process.exit(1);
  console.log('ok');
})();
`
	f := filepath.Join(t.TempDir(), "definition_dialog.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The conversation's answer: the reply, and the change it hands over, if
// any. One that is not the JSON asked for is a reply with no change.
func TestTheDefinitionConversationIsRead(t *testing.T) {
	for _, c := range []struct{ raw, reply, change string }{
		{`{"reply": "Done thinking.", "change": "Add a step."}`, "Done thinking.", "Add a step."},
		{"```json\n{\"reply\": \"Fenced.\", \"change\": \"\"}\n```", "Fenced.", ""},
		{"Just words, no JSON.", "Just words, no JSON.", ""},
	} {
		reply, change := parseDefinitionAssist(c.raw)
		if reply != c.reply || change != c.change {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.raw, reply, change, c.reply, c.change)
		}
	}
}

// One ✨ per page, as on the agent editor: the machine's and the pipeline's
// editors carry no form or field helper of their own beside the page's
// ✨ Get assistance, which reaches every step and stage.
func TestTheDefinitionEditorsHaveOneAssistant(t *testing.T) {
	for _, f := range []string{"machine_editor.go", "machine_page.go", "pipeline_page.go"} {
		src := readFile(t, f)
		for _, helper := range []string{"Assist:", "SuggestURL:", "AssistPrompt:"} {
			if strings.Contains(src, helper) {
				t.Errorf("%s still gives a form or field its own ✨ (%s)", f, helper)
			}
		}
	}
}
