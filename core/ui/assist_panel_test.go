package ui

// The assist panel beside a form: it opens on its request, the model is told
// the form and what it holds, what it proposes fills the form as a version
// that walks back to what was there, and the window says what the endpoint
// is doing while it works. Driven, because each of these fails silently: a
// form that is not filled, or a request without the form, raises nothing.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistPanelFillsTheForm(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + `
global.setTimeout = function(fn) { fn(); };
// A form as form_panel exposes it: values() and fill().
var formVals = {steps: [{command: 'heif-dec {in} {out}'}], name: 'heic-adapted'};
components.fake_form = function(cfg) {
  var f = node('div'); f.className = 'ui-form';
  f.uiForm = {
    values: function() { return JSON.parse(JSON.stringify(formVals)); },
    fill: function(v) { formVals = JSON.parse(JSON.stringify(v)); },
  };
  return f;
};
var sent = null, progressSeen = [];
global.fetch = function(url, o) {
  sent = {url: url, body: JSON.parse(o.body)};
  return Promise.resolve({ok: true, text: function() {
    return Promise.resolve('{"progress":"check program: heif-convert"}\n' +
      '{"reply":"heif-convert is here.","fields":{"steps":[{"command":"heif-convert {in} {out}"}]}}\n');
  }});
};
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
function texts(n, out) { out = out || []; if (n.textContent) out.push(n.textContent); if (n.innerHTML) out.push(n.innerHTML); (n.children || []).forEach(function(c) { texts(c, out); }); return out; }

var panel = components.assist_panel({
  url: 'api/assist?id=read.heic', ask: 'Why does it fail?', web: true,
  work: {type: 'fake_form', fields: [
    {field: 'name', label: 'Name', type: 'text'},
    {field: 'steps', label: 'Steps', type: 'rows', columns: [{field: 'command', label: 'Command'}]},
    {field: 'id', type: 'hidden'},
  ]},
});
setImmediate(function() { setImmediate(function() {
  if (!sent) throw new Error('the opening request was never sent');
  if (sent.url !== 'api/assist?id=read.heic') throw new Error('sent to ' + sent.url);
  var b = sent.body;
  if (b.message !== 'Why does it fail?') throw new Error('message: ' + b.message);
  if (b.web !== false) throw new Error('web should go out off until ticked: ' + b.web);
  if (!b.form || b.form.length !== 2) throw new Error('the form should travel without its hidden field: ' + JSON.stringify(b.form));
  if (b.form[1].columns[0].field !== 'command') throw new Error('a list field should carry its columns');
  if (b.values.steps[0].command !== 'heif-dec {in} {out}') throw new Error('the values should travel');
  if (formVals.steps[0].command !== 'heif-convert {in} {out}') throw new Error('the proposal did not fill the form: ' + JSON.stringify(formVals));
  if (formVals.name !== 'heic-adapted') throw new Error('a field it did not propose changed');
  var all = texts(panel).join(' | ');
  if (all.indexOf('heif-convert is here.') < 0) throw new Error('the reply is not shown: ' + all);
  if (all.indexOf('v2 of 2') < 0) throw new Error('the proposal should be a version: ' + all);
  // Walk back: what was there before comes back into the form.
  var navs = [];
  (function walk(n) { if (n.className === 'ui-assist-nav') navs.push(n); (n.children || []).forEach(walk); })(panel);
  fire(navs[0]);
  if (formVals.steps[0].command !== 'heif-dec {in} {out}') throw new Error('walking back did not restore the form');
  if (!find(panel, 'ui-assist-web')) throw new Error('web was offered but no switch shows');
  console.log('OK');
}); });
`
	tmp := filepath.Join(t.TempDir(), "panel.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist panel:\n%s", out)
	}
}

// The panel's Go shape: the work rides as a nested component, and the row
// action carries the ✨ and opens wide.
func TestAssistPanelMarshals(t *testing.T) {
	a := AssistAction("Fix", AssistPanel{Work: FormPanel{Source: "api/x?id={id}"}, URL: "api/x/assist?id={id}", Web: true})
	if a.Label != "✨ Fix" || a.Width != AssistWidth || a.Type != "modal" {
		t.Fatalf("row action: %+v", a)
	}
	var got map[string]any
	if err := json.Unmarshal(a.Render, &got); err != nil {
		t.Fatal(err)
	}
	work, _ := got["work"].(map[string]any)
	if got["type"] != "assist_panel" || got["url"] != "api/x/assist?id={id}" || got["web"] != true || work["type"] != "form_panel" {
		t.Fatalf("marshalled as %s", a.Render)
	}
}

// The real form panel is what the panel reads and fills: it has to expose
// the handle, and a fill has to redraw the fields and count as a change
// (unsaved until Save), not save on its own.
func TestTheFormExposesWhatAnAssistFills(t *testing.T) {
	basics := readRuntimeFile(t, "10_basics.js")
	at := strings.Index(basics, "components.form_panel = function")
	if at < 0 {
		t.Fatal("form_panel not found")
	}
	head := basics[at : at+1600]
	for _, want := range []string{"wrap.uiForm = {", "values: function()", "fill: function(vals)", "render();", "new Event('change')"} {
		if !strings.Contains(head, want) {
			t.Errorf("form_panel's assist handle lacks %q", want)
		}
	}
	if strings.Contains(head[strings.Index(head, "fill: function"):strings.Index(head, "fill: function")+300], "save(") {
		t.Error("a fill should not save")
	}
}

// While a turn runs, Send is Stop, and Stop ends the turn even when the
// caller's send() never answers or ignores the signal; an answer that
// arrives afterwards is dropped.
func TestAssistStopEndsTheTurn(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + `
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
var late = null, sends = 0;
window.uiOpenAssist({initial: 'text', send: function(req, done) { sends++; late = done; }});
var wrap = find(document.body, 'ui-assist');
var composer = find(wrap, 'ui-assist-input'), btn = find(wrap, 'ui-assist-send');
composer.value = 'shorten it';
fire(btn);
if (btn.textContent !== 'Stop' || !composer.disabled) throw new Error('a running turn should show Stop');
fire(btn);
if (btn.textContent !== 'Send' || composer.disabled) throw new Error('Stop did not end the turn');
late({reply: 'too late', value: 'changed'});
var log = find(wrap, 'ui-assist-log');
var said = log.children.map(function(c) { return c.textContent || c.innerHTML; }).join(' | ');
if (said.indexOf('too late') >= 0) throw new Error('an answer after Stop was shown: ' + said);
if (said.indexOf('Stopped.') < 0) throw new Error('Stop should say so: ' + said);
if (find(wrap, 'ui-assist-draft').value !== 'text') throw new Error('an answer after Stop changed the draft');
composer.value = 'again';
fire(btn);
if (sends !== 2) throw new Error('after Stop a new turn should go out');
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "stop.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist Stop:\n%s", out)
	}
}

// A walk goes through the records in turn, the panel for each with its
// placeholders filled from it; saving moves on, and from the last closes.
func TestAssistWalkGoesThroughTheRecords(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + `
function fetchJSON(url) { return Promise.resolve([{id: 'read.heic', enables: 'Reading HEIC'}, {id: 'media.transcribe', enables: 'Transcribing'}]); }
function substitute(s, rec) { return s.replace(/\{([a-z_]+)\}/g, function(m, k) { return rec[k] == null ? m : encodeURIComponent(rec[k]); }); }
` + src + `
global.setTimeout = function(fn) { fn(); };
var shown = [], saveHook = null;
components.fake_form = function(cfg, ctx) {
  shown.push(cfg.source);
  saveHook = ctx.__closeModal;
  var f = node('div'); f.className = 'ui-form';
  f.uiForm = {values: function() { return {}; }, fill: function() {}};
  return f;
};
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
var closed = false;
var walk = components.assist_panel({
  url: 'api/fix?id={id}', walk: {source: 'api/rows?fix=1', label: 'enables'},
  work: {type: 'fake_form', source: 'api/adapt?id={id}'},
}, {__closeModal: function() { closed = true; }});
setImmediate(function() {
  var at = find(walk, 'ui-assist-walk-at');
  if (!at || String(at.children[0]) !== '1 of 2: Reading HEIC') throw new Error('where it is: ' + JSON.stringify(at && at.children[0]));
  if (shown[0] !== 'api/adapt?id=read.heic') throw new Error('the first record\'s form: ' + shown[0]);
  saveHook();
  if (shown[1] !== 'api/adapt?id=media.transcribe') throw new Error('saving should move on: ' + shown.join(','));
  if (closed) throw new Error('it closed mid-walk');
  saveHook();
  if (!closed) throw new Error('saving the last should close the dialog');
  console.log('OK');
});
`
	tmp := filepath.Join(t.TempDir(), "walk.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist walk:\n%s", out)
	}
}

// A file attached with the 📎 goes with the next turn, once; every turn
// names the window, so the endpoint keeps the file for the conversation.
func TestAssistAttachSendsAFileOnce(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + `
global.FileReader = function() {
  var self = this;
  this.readAsDataURL = function(f) { self.result = 'data:x;base64,' + f.b64; self.onload(); };
};
function find(n, pred) {
  if (pred(n)) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], pred); if (f) return f; }
  return null;
}
function cls(c) { return function(n) { return n.className && (' ' + n.className + ' ').indexOf(' ' + c + ' ') >= 0; }; }
function fire(n, k) { (n['__' + (k || 'click')] || []).forEach(function(f) { f(); }); }
var reqs = [];
var chat = window.uiAssistChat({attach: true, send: function(req, done) { reqs.push(req); done({reply: 'ok'}); }});
var picker = find(chat.right, function(n) { return n.type === 'file'; });
if (!picker) throw new Error('no file picker');
picker.files = [{name: 'photo.heic', b64: 'QUJD'}];
fire(picker, 'change');
if (find(chat.right, cls('ui-assist-files')).children.length !== 1) throw new Error('the picked file should show');
chat.say('try this');
setImmediate(function() {
  if (reqs.length !== 1 || !reqs[0].files || reqs[0].files[0].name !== 'photo.heic' || reqs[0].files[0].data !== 'QUJD') throw new Error('the file did not go: ' + JSON.stringify(reqs[0]));
  if (!reqs[0].window) throw new Error('the turn names no window');
  chat.say('again');
  setImmediate(function() {
    if (reqs[1].files) throw new Error('the file went twice');
    if (reqs[1].window !== reqs[0].window) throw new Error('the window changed');
    console.log('OK');
  });
});
`
	tmp := filepath.Join(t.TempDir(), "attach.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the assist attach:\n%s", out)
	}
}
