package ui

// A note written before the page arrived (another session, then back) has no
// bubble on it. When the agent picks the note up, the page draws it from the
// event, in the place it was read, instead of saying a note was read and
// showing none. A note the page already has, or replayed from history, is not
// drawn twice.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// boundedRuntime returns src from the first line containing start to the end of the
// block opened there (the first line equal to closer after it).
func boundedRuntime(t *testing.T, src, start, closer string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("%q is gone", start)
	}
	j := strings.Index(src[i:], closer)
	if j < 0 {
		t.Fatalf("could not bound %q", start)
	}
	return src[i : i+j+len(closer)]
}

func TestAPickedUpNoteThePageLacksIsDrawn(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	renderer := boundedRuntime(t, readRuntimeFile(t, "35_ask_cards.js"),
		"window.uiRegisterBlockRenderer('ui_notes_consumed'", "\n    });")
	panel := readRuntimeFile(t, "30_agent_loop_panel.js")
	adder := boundedRuntime(t, panel, "function addPickedUpNote(", "\n    }\n") +
		boundedRuntime(t, panel, "function claimOwnNoteBubble(", "\n    }\n")

	harness := `
var registered;
var window = {CSS: null, uiRegisterBlockRenderer: function(n, fn) { registered = fn; }};
function el(tag, attrs, kids) { return {tag: tag, kids: kids}; }
// A page: bubbles by note id, plus the panel's msgEls.
var byNote = {};
var document = {querySelector: function(sel) {
  var m = /data-note-id="([^"]*)"/.exec(sel);
  var b = m && byNote[m[1]];
  if (!b) return null;
  if (sel.indexOf('.ui-agent-interjection') === 0 && b.classList.list.indexOf('ui-agent-interjection') < 0) return null;
  return b;
}};
function bubble(classes) {
  var b = {dataset: {}, classList: {list: (classes || []).slice(),
    add: function() { for (var i = 0; i < arguments.length; i++) if (this.list.indexOf(arguments[i]) < 0) this.list.push(arguments[i]); },
    remove: function(c) { this.list = this.list.filter(function(x) { return x !== c; }); },
    contains: function(c) { return this.list.indexOf(c) >= 0; }},
    querySelector: function() { return null; }};
  return b;
}
var msgEls = {}, drawn = [];
function addMessage(role, id, text) {
  var b = bubble();
  msgEls[id] = {bubble: b, role: role, rawText: text};
  drawn.push(text);
}
` + adder + `
` + renderer + `
function fire(d) {
  var out = registered(d, {addNote: function(id, text) { addPickedUpNote(id, text); var e = msgEls['note-' + id]; if (e) byNote[id] = e.bubble; }});
  return out;
}

// 1. The page has no bubble for the note: it is drawn, read, with its id.
var out = fire({ids: ['n1'], notes: [{id: 'n1', text: 'check the disk too'}]});
if (drawn.join('|') !== 'check the disk too') throw new Error('the missing note was not drawn: ' + drawn.join('|'));
var b = msgEls['note-n1'].bubble;
if (!b.classList.contains('consumed') || b.dataset.noteId !== 'n1') throw new Error('the drawn note is not marked read');
if (!out || !out.wrap) throw new Error('the picked-up line is gone');

// 2. The same event again (a replay): nothing new.
fire({ids: ['n1'], notes: [{id: 'n1', text: 'check the disk too'}]});
if (drawn.length !== 1) throw new Error('a replay drew the note twice');

// 3. A note already on the page from history (data-note-id): not drawn.
byNote['n2'] = bubble();
fire({ids: ['n2'], notes: [{id: 'n2', text: 'from history'}]});
if (drawn.length !== 1) throw new Error('a note replayed from history was drawn again');

// 4. The page's own note, picked up before its POST returned the id: claimed.
addMessage('user', 'u-1', 'my own note');
msgEls['u-1'].bubble.classList.add('ui-agent-interjection');
fire({ids: ['n3'], notes: [{id: 'n3', text: 'my own note'}]});
if (drawn.length !== 2) throw new Error('the page drew its own pending note a second time');
if (msgEls['u-1'].bubble.dataset.noteId !== 'n3' || !msgEls['u-1'].bubble.classList.contains('consumed')) throw new Error('the pending note was not claimed');

// 5. An old event with no text draws nothing.
fire({ids: ['n4']});
if (drawn.length !== 2) throw new Error('an event without text drew something');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "notes.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("picked-up notes:\n%s", out)
	}
}

// A page joining a running turn draws the notes still waiting, as waiting
// notes, once each, and not over a note it already has.
func TestWaitingNotesAreDrawnOnJoin(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	claim := boundedRuntime(t, src, "function claimOwnNoteBubble(", "\n    }\n")
	show := boundedRuntime(t, src, "function showWaitingNotes(", "\n    }\n")

	harness := `
var window = {CSS: null};
var cfg = {inject_url: 'api/inject'};
var activeSessionId = 's1', asked = [];
var reply = {notes: [{id: 'n1', text: 'check the disk too'}, {id: 'n2', text: 'mine'}]};
function fetchJSON(url) { asked.push(url); return {then: function(f) { f(reply); return {catch: function() {}}; }}; }
function bubble(classes) {
  return {dataset: {}, classList: {list: (classes || []).slice(),
    add: function() { for (var i = 0; i < arguments.length; i++) if (this.list.indexOf(arguments[i]) < 0) this.list.push(arguments[i]); },
    contains: function(c) { return this.list.indexOf(c) >= 0; }}};
}
var msgEls = {}, drawn = [], pinned = 0;
function addMessage(role, id, text) { msgEls[id] = {bubble: bubble(), role: role, rawText: text}; drawn.push(text); }
function keepPendingInterjectionsLast() { pinned++; }
var convoLog = {querySelector: function(sel) {
  var m = /data-note-id="([^"]*)"/.exec(sel);
  for (var k in msgEls) if (msgEls[k].bubble.dataset.noteId === m[1]) return msgEls[k].bubble;
  return null;
}};
` + claim + `
` + show + `
// The page's own note, sent before its id came back.
addMessage('user', 'u-1', 'mine');
msgEls['u-1'].bubble.classList.add('ui-agent-interjection');
drawn = [];

showWaitingNotes('s1');
if (asked[0] !== 'api/inject?id=s1') throw new Error('asked ' + asked[0]);
if (drawn.join('|') !== 'check the disk too') throw new Error('drawn: ' + drawn.join('|'));
var b = msgEls['note-n1'].bubble;
if (!b.classList.contains('ui-agent-interjection') || b.classList.contains('consumed')) throw new Error('not drawn as a waiting note');
if (b.dataset.noteId !== 'n1' || b.dataset.sessionId !== 's1') throw new Error('waiting note not tagged');
if (msgEls['u-1'].bubble.dataset.noteId !== 'n2') throw new Error('own pending note not claimed');
if (!pinned) throw new Error('waiting notes not pinned to the bottom');

showWaitingNotes('s1');
if (drawn.length !== 1) throw new Error('a second join drew the notes again');

activeSessionId = 's2';
reply = {notes: [{id: 'n9', text: 'late'}]};
showWaitingNotes('s1');
if (drawn.length !== 1) throw new Error('drew notes for a session the page has left');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "waiting.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("waiting notes:\n%s", out)
	}
}
