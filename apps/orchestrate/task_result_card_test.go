package orchestrate

// A finished background task's result is the agent's own reply in the
// conversation that started the work. It was posted, drawn and read back as a
// scheduled report: a ⏰ card that looked like a recurring task had answered
// the person, and in the agent's history an "automated report" it had not
// said, so it lost track of the picture it had already sent.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAFinishedTaskPostsTheAgentsOwnReply(t *testing.T) {
	if k := fireCardKind(orchUpdatePayload{Prompt: taskWakeMarker + " rendering a picture"}); k != cortexKindResult {
		t.Errorf("a task's result posts as %q", k)
	}
	if k := fireCardKind(orchUpdatePayload{Prompt: "check the feed"}); k != cortexKindScheduled {
		t.Errorf("a recurring fire posts as %q", k)
	}
}

func TestTheAgentReadsItsResultAsItsOwnWords(t *testing.T) {
	result := ChatMessage{Role: "assistant", ReportFrom: "background task", ReportKind: cortexKindResult, Content: "Here it is."}
	if got := llmHistoryContent(result); got != "Here it is." {
		t.Errorf("the agent's own reply was fenced as a report: %q", got)
	}
	report := ChatMessage{Role: "assistant", ReportFrom: "check feed", ReportKind: cortexKindScheduled, Content: "3 new"}
	if got := llmHistoryContent(report); !strings.HasPrefix(got, "<gohort-meta>") {
		t.Errorf("a schedule's report lost its marker: %q", got)
	}
	msgs := []ChatMessage{report, result}
	if got := priorReportsFrom(msgs); len(got) != 1 || !strings.HasPrefix(got[0], "check feed") {
		t.Errorf("the judge's earlier reports should leave the agent's own reply out: %v", got)
	}
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u-result")
	if _, err := saveChatSession(udb, ChatSession{ID: cortexSessionID("a1"), AgentID: "a1", Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	if lines := cortexContextLines(udb, "a1"); len(lines) != 1 || !strings.Contains(lines[0], "check feed") {
		t.Errorf("standing activity should list what reached the agent, not what it said: %v", lines)
	}
}

// The card hook, run: a result keeps the agent's bubble with a quiet note;
// a schedule's report still gets its card. Skips where node is not installed.
func TestAResultIsDrawnAsTheAgentsBubble(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	i, j := strings.Index(ReportCardHead, "<script>"), strings.LastIndex(ReportCardHead, "</script>")
	if i < 0 || j < i {
		t.Fatal("the report card has no script")
	}
	harness := `
function node(tag) {
  var n = {tag: tag, kids: [], className: '', textContent: '', classList: {list: [], add: function() { for (var i = 0; i < arguments.length; i++) this.list.push(arguments[i]); }},
    appendChild: function(c) { this.kids.push(c); return c; },
    insertBefore: function(c) { this.kids.unshift(c); return c; },
    querySelector: function(sel) {
      var names = sel.split(',').map(function(s) { return s.replace(':scope > .', '').trim(); });
      for (var i = 0; i < this.kids.length; i++) if (names.indexOf(this.kids[i].className) >= 0) return this.kids[i];
      return null;
    }};
  return n;
}
var document = {createElement: node};
var hooks = [];
var window = {uiRegisterMessageReplayHook: function(f) { hooks.push(f); }};
function setTimeout() {}
` + ReportCardHead[i+len("<script>"):j] + `
var fail = 0;
function check(label, cond) { if (!cond) { fail++; console.log('FAIL ' + label); } }
var result = node('div'), report = node('div');
hooks.forEach(function(h) { h(result, {report_from: 'background task', report_kind: 'result', created: 0}); });
hooks.forEach(function(h) { h(report, {report_from: 'check feed', report_kind: 'scheduled', created: 0}); });
check('a result keeps the agent bubble, no card frame', result.classList.list.indexOf('orch-report-card') < 0);
check('a result says when it came', result.kids.length === 1 && result.kids[0].className === 'orch-result-note');
check('a schedule report is still a card', report.classList.list.indexOf('orch-report-card') >= 0 && report.kids[0].className === 'orch-report-card-hdr');
hooks.forEach(function(h) { h(result, {report_from: 'background task', report_kind: 'result'}); });
check('drawing it twice adds nothing', result.kids.length === 1);
if (fail) process.exit(1);
console.log('ok');
`
	f := filepath.Join(t.TempDir(), "result_card.js")
	os.WriteFile(f, []byte(harness), 0o644)
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
