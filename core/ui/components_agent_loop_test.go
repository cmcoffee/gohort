package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeFunc cuts one top-level function of the agent loop panel's runtime
// out of the file, so a test can run the real code under node.
func runtimeFunc(t *testing.T, src, name string) string {
	t.Helper()
	i := strings.Index(src, "function "+name+"(")
	if i < 0 {
		t.Fatalf("%s is gone from the runtime", name)
	}
	end := strings.Index(src[i:], "\n    }")
	if end < 0 {
		t.Fatalf("could not bound %s", name)
	}
	return src[i : i+end+len("\n    }")]
}

// A bubble is never drawn as an empty card. The live case: a long reply held
// back beside a tool call had its text blanked, and its bubble stayed on
// screen for the tool pill it carried, as a blank card with a hover-only
// "1 tool" button. A shown bubble with no text now drops the body that draws
// the card (.ui-agent-msg-textless), whichever way it got there; a bubble
// with nothing at all is still hidden; and text arriving puts it back.
func TestABubbleIsNeverAnEmptyCard(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	var fns []string
	for _, name := range []string{"unmarkEmptyBubble", "markTextless", "markEmptyBubble", "agentMsgAttachmentBox"} {
		fns = append(fns, runtimeFunc(t, src, name))
	}

	harness := `
// A bubble just real enough: a class list, direct children by class, and the
// selectors the guard uses.
function classList() {
  var set = [];
  return {
    add: function() { for (var i = 0; i < arguments.length; i++) if (set.indexOf(arguments[i]) < 0) set.push(arguments[i]); },
    remove: function() { for (var i = 0; i < arguments.length; i++) { var at = set.indexOf(arguments[i]); if (at >= 0) set.splice(at, 1); } },
    toggle: function(c, on) { if (on) this.add(c); else this.remove(c); },
    contains: function(c) { return set.indexOf(c) >= 0; },
  };
}
function child(cls, text) { return {cls: cls, textContent: text || ''}; }
function bubble(children) {
  return {
    classList: classList(),
    children: children,
    appendChild: function(n) { var at = this.children.indexOf(n); if (at >= 0) this.children.splice(at, 1); this.children.push(n); },
    querySelector: function(sel) {
      var wanted = sel.split(',').map(function(s) { return s.trim().replace(':scope > .', ''); });
      for (var i = 0; i < this.children.length; i++) {
        if (wanted.indexOf(this.children[i].cls) >= 0) return this.children[i];
      }
      return null;
    },
  };
}
function el(tag, attrs) { return child(attrs['class'], ''); }
function is(b, cls) { return b.classList.contains(cls); }
` + strings.Join(fns, "\n") + `

// The live case: text blanked under a tool pill.
var body = child('ui-agent-msg-body', '');
var b = bubble([body, child('ui-agent-tools-panel')]);
markEmptyBubble({bubble: b});
if (is(b, 'ui-agent-msg-empty')) throw new Error('a bubble carrying a tool pill was hidden, and its pill with it');
if (!is(b, 'ui-agent-msg-textless')) throw new Error('a blanked bubble with a tool pill was left as an empty card');

// Text arrives: an ordinary bubble again.
body.textContent = 'Here is the answer.';
unmarkEmptyBubble({bubble: b});
if (is(b, 'ui-agent-msg-textless') || is(b, 'ui-agent-msg-empty')) throw new Error('a bubble with text kept a textless or empty mark');

// Nothing at all: hidden, as before.
var bare = bubble([child('ui-agent-msg-body', '   ')]);
markEmptyBubble({bubble: bare});
if (!is(bare, 'ui-agent-msg-empty')) throw new Error('a bubble with nothing in it was shown');

// A delivery before any text: shown, with no empty card above it.
var lazy = bubble([child('ui-agent-msg-body', '')]);
lazy.classList.add('ui-agent-msg-empty');
agentMsgAttachmentBox(lazy);
if (is(lazy, 'ui-agent-msg-empty')) throw new Error('a delivered attachment stayed hidden');
if (!is(lazy, 'ui-agent-msg-textless')) throw new Error('an attachment with no text was drawn under an empty card');

// A delivery into a bubble that has text is an ordinary bubble.
var said = bubble([child('ui-agent-msg-body', 'Here it is.')]);
agentMsgAttachmentBox(said);
if (is(said, 'ui-agent-msg-textless')) throw new Error('a bubble with text was drawn without its body');

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "textless.js")
	if err := os.WriteFile(tmp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("a bubble can still be an empty card:\n%s", out)
	}

	// And the class has to mean something on screen: no body (the card),
	// the tools button in view.
	css, err := os.ReadFile(filepath.Join("assets", "runtime.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		".ui-agent-msg-textless > .ui-agent-msg-body { display: none; }",
		".ui-agent-msg-textless > .ui-agent-msg-actions { opacity: 1;",
	} {
		if !strings.Contains(string(css), rule) {
			t.Errorf("runtime.css is missing %q", rule)
		}
	}
}
