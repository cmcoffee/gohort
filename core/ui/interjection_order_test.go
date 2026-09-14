package ui

// A note written mid-turn waits at the bottom until the agent picks it up.
//
// The runner drains the queue between ROUNDS, so whatever is being written when
// the user presses send was decided without their note. Anything that lands
// under the note claims to be a reply to it; keeping the note last says what is
// actually true, and once it is delivered the ordering stops being a claim.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAQueuedNoteWaitsAtTheBottom(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function keepPendingInterjectionsLast()")
	if i < 0 {
		t.Fatal("the pending-note pin is gone")
	}
	end := strings.Index(src[i:], "\n    }")
	if end < 0 {
		t.Fatal("could not bound keepPendingInterjectionsLast")
	}
	fn := src[i : i+end+len("\n    }")]

	harness := `
// A convoLog just real enough: children in order, classes per node, and the
// one selector the pin uses. appendChild MOVES an existing child, which is the
// behaviour the pin depends on.
function node(name, classes) {
  return {name: name, classes: (classes || []).slice()};
}
var convoLog = {
  children: [],
  appendChild: function(n) {
    var at = this.children.indexOf(n);
    if (at >= 0) this.children.splice(at, 1);
    this.children.push(n);
  },
  querySelectorAll: function(sel) {
    if (sel !== '.ui-agent-interjection:not(.consumed)') throw new Error('unexpected selector: ' + sel);
    return this.children.filter(function(n) {
      return n.classes.indexOf('ui-agent-interjection') >= 0 &&
             n.classes.indexOf('consumed') < 0;
    });
  }
};
function order() { return convoLog.children.map(function(n) { return n.name; }).join(','); }
` + fn + `

var note = node('note', ['ui-agent-interjection']);
convoLog.children = [node('turn'), node('streaming'), note];

// The agent keeps working. Each new bubble goes under the streaming one, and
// the waiting note stays below all of it.
convoLog.appendChild(node('tool-round'));
keepPendingInterjectionsLast();
if (order() !== 'turn,streaming,tool-round,note') throw new Error('note did not stay last: ' + order());

convoLog.appendChild(node('more-output'));
keepPendingInterjectionsLast();
if (order() !== 'turn,streaming,tool-round,more-output,note') throw new Error('note drifted upward: ' + order());

// A second note queued behind the first keeps the order they were written in.
var note2 = node('note2', ['ui-agent-interjection']);
convoLog.appendChild(note2);
convoLog.appendChild(node('yet-more'));
keepPendingInterjectionsLast();
if (order() !== 'turn,streaming,tool-round,more-output,yet-more,note,note2') throw new Error('two pending notes lost their order: ' + order());

// Delivered. From here the note holds its place and what the agent says next
// appears BELOW it — which is now a true claim rather than a misleading one.
note.classes.push('consumed');
note2.classes.push('consumed');
convoLog.appendChild(node('the-reply'));
keepPendingInterjectionsLast();
if (order() !== 'turn,streaming,tool-round,more-output,yet-more,note,note2,the-reply') throw new Error('a delivered note was still being moved: ' + order());

// Nothing queued is the ordinary case and must not reorder anything.
convoLog.children = [node('a'), node('b')];
keepPendingInterjectionsLast();
if (order() !== 'a,b') throw new Error('the pin touched a conversation with no pending note: ' + order());

console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "interject.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("a queued note does not wait at the bottom:\n%s", out)
	}
}

// The pin has to run wherever the conversation grows, or a note stays put while
// content lands under it — the exact thing it exists to prevent.
func TestEveryConvoAppendRepinsPendingNotes(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	if n := strings.Count(src, "keepPendingInterjectionsLast()"); n < 5 {
		t.Errorf("the pin is called %d times; the live-turn append sites (message, block, status note, error, submit) plus its own definition should all be covered", n)
	}
	// And the move-above-the-stream it replaced must not come back: the two
	// place the note on opposite sides of the same output.
	if strings.Contains(src, "convoLog.insertBefore(nb, anchor)") {
		t.Error("the old move-above-the-in-flight-bubble is back; it puts a note above output that was written before the agent had read it")
	}
}
