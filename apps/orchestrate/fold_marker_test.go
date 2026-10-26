package orchestrate

// A thread shows where a history fold stopped the agent reading word for
// word. The page kept every message on screen while the agent worked from a
// summary of most of them, so it looked as if the agent had forgotten what
// was plainly there. The fold said so only in the trail.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheFoldIsMarkedWhereTheAgentStopsReading(t *testing.T) {
	thread := func(n int) []ChatMessage {
		out := make([]ChatMessage, n)
		for i := range out {
			out[i] = ChatMessage{Role: "user", Content: "m"}
		}
		return out
	}
	marked := func(msgs []ChatMessage) int {
		for i, m := range msgs {
			if m.FoldBefore != nil {
				return i
			}
		}
		return -1
	}
	// Served from the start: marked at the fold point.
	tail := thread(30)
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 0)
	if i := marked(tail); i != 26 || tail[26].FoldBefore.Summarized != 26 {
		t.Errorf("marked at %d, want 26", i)
	}
	// Served from index 20: the same message, at its place in the tail.
	tail = thread(10)
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 20)
	if i := marked(tail); i != 6 {
		t.Errorf("in a tail served from 20, marked at %d, want 6", i)
	}
	// A hidden message at the fold point passes the mark on to the next one shown.
	tail = thread(30)
	tail[26].Hidden = true
	markFoldPoint(CompactState{SummarizedThrough: 26}, tail, 0)
	if i := marked(tail); i != 27 {
		t.Errorf("past a hidden message, marked at %d, want 27", i)
	}
	// Not in what was served, or nothing folded: no mark.
	for _, c := range []struct{ through, n, off int }{{26, 10, 30}, {0, 30, 0}, {10, 10, 20}} {
		tail = thread(c.n)
		markFoldPoint(CompactState{SummarizedThrough: c.through}, tail, c.off)
		if i := marked(tail); i != -1 {
			t.Errorf("through %d, served %d from %d: marked at %d, want none", c.through, c.n, c.off, i)
		}
	}
}

// End to end: the session a page loads carries the mark, and only there.
func TestALoadedThreadCarriesTheFoldMark(t *testing.T) {
	app, req, udb := authedApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "ag", Owner: "alice", Name: "Wiwee", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	seedFoldSession(t, udb, "ag", "s1", 30)
	saveCompactState(udb, "ag", "s1", CompactState{SummarizedThrough: 26, FoldSeq: 1})
	w := httptest.NewRecorder()
	routed(app)(w, req(http.MethodGet, "/api/sessions/s1?agent_id=ag", nil))
	if w.Code != 200 {
		t.Fatalf("GET %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Messages      []map[string]any `json:"messages"`
		MessageOffset int              `json:"message_offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var at []int
	for i, m := range got.Messages {
		if m["fold_before"] != nil {
			at = append(at, i+got.MessageOffset)
		}
	}
	if len(at) != 1 || at[0] != 26 {
		t.Errorf("the mark is on message(s) %v, want just 26", at)
	}
	sess, _ := loadChatSession(udb, "ag", "s1")
	for _, m := range sess.Messages {
		if m.FoldBefore != nil {
			t.Fatal("the mark was stored on the thread")
		}
	}
}

// The page draws it: a line above the first message the agent still reads
// word for word, once however often the thread is redrawn.
func TestThePageDrawsTheFoldMark(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	js := ReportCardHead[strings.Index(ReportCardHead, "<script>")+len("<script>") : strings.LastIndex(ReportCardHead, "</script>")]
	script := `
var hooks = [];
global.window = {uiRegisterMessageReplayHook: function(f) { hooks.push(f); }};
global.document = {createElement: function(tag) { return {tagName: tag, classList: {list: [], add: function(c) { this.list.push(c); }, contains: function(c) { return this.list.indexOf(c) >= 0; }}, set className(v) { this.classList.list = v.split(' '); }}; }};
` + js + `
var inserted = [];
var list = {insertBefore: function(n, ref) { inserted.push(n); ref.previousElementSibling = n; }};
var bubble = {parentNode: list, previousElementSibling: null};
hooks.forEach(function(h) { h(bubble, {role: 'user', content: 'x', fold_before: {summarized: 26}}); });
hooks.forEach(function(h) { h(bubble, {role: 'user', content: 'x', fold_before: {summarized: 26}}); });
var plain = {parentNode: list, previousElementSibling: null};
hooks.forEach(function(h) { h(plain, {role: 'user', content: 'y'}); });
if (inserted.length !== 1) { console.log('FAIL drew ' + inserted.length + ' marks'); process.exit(1); }
if (inserted[0].textContent.indexOf('26 messages above are summarized') < 0 || !inserted[0].classList.contains('orch-fold-mark')) {
  console.log('FAIL the mark reads ' + JSON.stringify(inserted[0].textContent)); process.exit(1);
}
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "fold.js")
	if err := os.WriteFile(tmp, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("node: %v\n%s", err, out)
	}
}
