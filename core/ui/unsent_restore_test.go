package ui

// A send the server never took comes back to the composer.
//
// A new thread has no id until the server answers its first send, so a send
// lost on the way (a restart, a dropped connection) left nothing behind: the
// composer was already cleared, the bubble went with the next reload, and the
// pasted picture had to be found and pasted again.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// panelFunc is one 4-space-indented function of the agent loop panel, from its
// header to its closing brace.
func panelFunc(t *testing.T, src, head string) string {
	t.Helper()
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("%q is gone", head)
	}
	end := strings.Index(src[i:], "\n    }")
	if end < 0 {
		t.Fatalf("could not bound %q", head)
	}
	return src[i : i+end+len("\n    }")]
}

func TestAnUnsentMessageComesBackToTheComposer(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	fns := panelFunc(t, src, "function restoreUnsent(") + "\n" +
		panelFunc(t, src, "function dispatchResponse(") + "\n" +
		panelFunc(t, src, "var attachmentsLoading = 0") + "\n"

	harness := `
global.Event = function(type) { this.type = type; };
var removed = [], activity = [], enabled = 0, rendered = 0, sends = 0, toasts = [];
function bubble(id) { return {id: id, parentNode: {}, remove: function() { removed.push(id); }}; }
var msgEls, pendingAttachments, inputArea;
function reset() {
  msgEls = {'u-1': {bubble: bubble('u-1')}};
  pendingAttachments = [];
  inputArea = {value: '', dispatchEvent: function() {}};
  removed = []; activity = []; enabled = 0;
}
function renderAttachments() { rendered++; }
function addActivity(kind, id, text) { activity.push(kind + ': ' + text); }
function enableInput() { enabled++; }
function showToast(m) { toasts.push(m); }
function sendMessage() { sends++; }
function streamLost() { activity.push('stream lost'); }
function streamSSE() { return Promise.resolve(); }
var cfg = {events_url: 'events', runs_url_base: ''}, activeRunId = '', sawTurnEnd = false, activeSessionId = '';
function subscribeEvents() {}
function ensureReportPolling() {}
function updateURLParam() {}
` + fns + `
function settle() { return new Promise(function(r) { setTimeout(r, 0); }); }
var pic = {name: 'pasted-image.png', kind: 'image', dataURL: 'data:image/png;base64,AA'};
(async function() {
  // Lost on the way: the browser's own words, the text and picture back.
  reset();
  inputArea.value = 'typed since';
  dispatchResponse(Promise.reject(new TypeError('Load failed')), {text: 'what is this', attachments: [pic], msgId: 'u-1'});
  await settle();
  if (inputArea.value !== 'what is this\n\ntyped since') throw new Error('text not restored ahead of what was typed since: ' + JSON.stringify(inputArea.value));
  if (pendingAttachments.length !== 1 || pendingAttachments[0] !== pic) throw new Error('the picture did not come back');
  if (removed.join() !== 'u-1' || msgEls['u-1']) throw new Error('the bubble of a message never sent stayed');
  if (activity.length !== 1 || activity[0].indexOf('could not reach the server') < 0 || activity[0].indexOf('back in the box') < 0) throw new Error('should say it was not sent: ' + JSON.stringify(activity));
  if (enabled !== 1) throw new Error('the composer was not given back');

  // Refused: the server's reason, and the message back.
  reset();
  dispatchResponse(Promise.resolve({ok: false, statusText: 'Bad Request', text: function() { return Promise.resolve('agent is paused'); }}),
    {text: 'hi', attachments: [], msgId: 'u-1'});
  await settle(); await settle();
  if (inputArea.value !== 'hi' || activity[0].indexOf('Not sent: agent is paused') < 0) throw new Error('a refused send: ' + JSON.stringify([inputArea.value, activity]));

  // Taken: a failure after the server answered is not "not sent".
  reset();
  dispatchResponse(Promise.resolve({ok: true, headers: {get: function() { return 'application/json'; }},
    json: function() { return Promise.reject(new Error('bad json')); }}), {text: 'hi', attachments: [pic], msgId: 'u-1'});
  await settle(); await settle();
  if (inputArea.value !== '' || pendingAttachments.length || removed.length) throw new Error('a send the server took was put back');

  // A Send pressed while a file is still being read goes when the last lands.
  attachmentsLoading = 2; sendWhenLoaded = true; sends = 0;
  attachmentLoaded();
  if (sends) throw new Error('sent before the last read landed');
  attachmentLoaded();
  if (sends !== 1 || sendWhenLoaded) throw new Error('did not send once the reads landed');
  attachmentsLoading = 1;
  attachmentLoaded();
  if (sends !== 1) throw new Error('a read with no Send waiting sent anyway');
  console.log('OK');
})().catch(function(e) { console.log(String(e)); process.exit(1); });
`
	tmp := filepath.Join(t.TempDir(), "unsent.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("an unsent message is not given back:\n%s", out)
	}
}
