package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole-agent dialog, run: its real script under node, over a stand-in
// DOM and a stand-in conversation pane. A reply with changes lists them to
// tick; a reply that only answers leaves them standing; Apply sends the
// ticked ones, triggers as a list, and reloads. Skips where node is not
// installed.
func TestTheAgentDialogListsAndAppliesChanges(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	s := agentAssistHTML("a1")
	start := strings.Index(s, "<script>")
	end := strings.Index(s, "</script>")
	if start < 0 || end < 0 {
		t.Fatal("the dialog's script is gone")
	}
	harness := `
function node(tag) {
  var n = {tag: tag, kids: [], attrs: {}, style: {}, dataset: {}, textContent: '', className: '',
    set innerHTML(v) { this.kids = []; },
    appendChild: function(c) { this.kids.push(c); return c; },
    insertBefore: function(c) { this.kids.unshift(c); return c; },
    setAttribute: function(k, v) { this.attrs[k] = v; },
    querySelectorAll: function(sel) {
      var out = [];
      (function walk(x) { x.kids.forEach(function(k) {
        if (k.tag === 'input' && k.attrs.type === 'checkbox') out.push(k);
        walk(k);
      }); })(this);
      return out;
    }};
  return n;
}
var slot = node('span');
var document = {createElement: node, getElementById: function() { return null; },
  querySelector: function(sel) { return sel === '.ui-section .ui-section-h-r' ? slot : null; }};
function requestAnimationFrame() {}
var modal = null, chatOpts = null, sent = null, reloaded = false;
var location = {reload: function() { reloaded = true; }};
function fetch(url, o) { sent = {url: url, method: o.method, body: JSON.parse(o.body)}; return Promise.resolve({ok: true}); }
var window = {
  uiOpenModal: function(o) { modal = o; },
  uiAssistChat: function(o) { chatOpts = o; return {right: node('div'), focus: function() {}}; },
  uiAssistPost: function(url) { return function() {}; },
  uiToast: function() {}, uiAlert: function() {},
};
` + s[start+len("<script>"):end] + `
var fail = 0;
function check(label, cond) { if (!cond) { fail++; console.log('FAIL ' + label); } }
var b = slot.kids[0];
check('the button is in the header, labelled', b && b.textContent === '✨ Get assistance');
b.onclick();
var body = node('div');
modal.mount(body, {close: function() {}});
var changes = body.kids[1], apply = body.kids[2].kids[0];
check('nothing proposed yet: no Apply', apply.style.display === 'none');
check('an answer with no changes says nothing of its own', chatOpts.onResult({reply: 'It has no rules.'}) === '');
var said = chatOpts.onResult({reply: '', changes: [
  {field: 'rules', label: 'Rules', why: 'stops guessing', value: 'Cite every claim.'},
  {field: 'triggers', label: 'Triggers', value: 'look up\n find out \n'}]});
check('proposed changes are listed to tick', changes.querySelectorAll().length === 2 && apply.style.display === '');
check('an empty reply with changes still says something', said !== '');
chatOpts.onResult({reply: 'Because it guesses.'});
check('a reply that only answers leaves the changes standing', changes.querySelectorAll().length === 2);
changes.querySelectorAll()[0].checked = false;
apply.onclick();
check('Apply sends the ticked ones as a PATCH', sent && sent.method === 'PATCH' && sent.url === '../api/agents/a1' &&
  !('rules' in sent.body) && JSON.stringify(sent.body.triggers) === '["look up","find out"]');
setTimeout(function() {
  check('and reloads to show what was stored', reloaded);
  if (fail) process.exit(1);
  console.log('ok');
}, 0);
`
	f := filepath.Join(t.TempDir(), "agent_dialog.js")
	os.WriteFile(f, []byte(harness), 0o644)
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
