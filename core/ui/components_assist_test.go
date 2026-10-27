package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The assist panel beside a form: it opens on its request, the model is told
// the form and what it holds, what it proposes fills the form as a version
// that walks back to what was there, and the window says what the endpoint
// is doing while it works. Driven, because each of these fails silently: a
// form that is not filled, or a request without the form, raises nothing.

func TestAssistPanelFillsTheForm(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + formStub + `
var sent = null;
global.fetch = function(url, o) {
  sent = {url: url, body: JSON.parse(o.body)};
  return Promise.resolve({ok: true, text: function() {
    return Promise.resolve('{"progress":"check program: heif-convert"}\n' +
      '{"reply":"heif-convert is here.","fields":{"steps":[{"command":"heif-convert {in} {out}"}],"key":"guess"}}\n');
  }});
};
var panel = components.assist_panel({url: 'api/assist?id=read.heic', ask: 'Why does it fail?', web: true, work: {type: 'fake_form'}});
var formEl = find(panel, 'ui-form');
setImmediate(function() { setImmediate(function() {
  if (!sent) throw new Error('the opening request was never sent');
  if (sent.url !== 'api/assist?id=read.heic') throw new Error('sent to ' + sent.url);
  var b = sent.body;
  if (b.message !== 'Why does it fail?') throw new Error('message: ' + b.message);
  if (b.web !== false) throw new Error('web should go out off until ticked: ' + b.web);
  if (!b.form || b.form.length !== 4) throw new Error('the form should travel without its hidden field: ' + JSON.stringify(b.form));
  var steps = b.form.filter(function(d) { return d.field === 'steps'; })[0];
  if (steps.columns[0].field !== 'command' || !steps.required) throw new Error('a list field carries its columns, and required: ' + JSON.stringify(steps));
  if (b.values.steps[0].command !== 'heif-dec {in} {out}') throw new Error('the values should travel');
  if (b.values.key !== '(set)' || JSON.stringify(b).indexOf('hunter2') >= 0) throw new Error('a secret travelled: ' + JSON.stringify(b.values));
  // The proposal fills the form, marked, and never the secret.
  if (formVals.steps[0].command !== 'heif-convert {in} {out}') throw new Error('the proposal did not fill the form: ' + JSON.stringify(formVals));
  if (formVals.key !== 'hunter2') throw new Error('a secret was filled');
  if (filledKeys.join(',') !== 'steps') throw new Error('what it filled should be marked: ' + filledKeys);
  // The helper is in, the form out, and the small version shows the change.
  var helper = find(panel, 'ui-assist-helper');
  if (helper.style.display === 'none' || formEl.style.display !== 'none') throw new Error('the helper should be swapped in for the form');
  var mini = find(panel, 'ui-assist-mini');
  var lit = find(mini, 'new');
  if (!lit || String(lit.children[1].children[0]) !== 'heif-convert {in} {out}') throw new Error('the change should be lit in the small form');
  var needed = find(mini, 'needed');
  if (!needed || String(needed.children[0].children[0]) !== 'Name') throw new Error('an empty required field should be marked needed');
  if (texts(panel).join(' | ').indexOf('heif-convert is here.') < 0) throw new Error('the reply is not shown');
  // Back to the form: the form in, the bar saying what it filled, Undo.
  fire(find(helper, 'ui-row-btn'));
  var bar = find(panel, 'ui-assist-bar');
  if (formEl.style.display === 'none' || helper.style.display !== 'none' || bar.style.display === 'none') throw new Error('Back to the form should swap them');
  if (texts(bar).join(' ').indexOf('It filled 1 field') < 0) throw new Error('the bar should say what it filled: ' + texts(bar).join(' '));
  var undo = bar.children.filter(function(c) { return c.children && String(c.children[0]) === 'Undo'; })[0];
  fire(undo);
  if (formVals.steps[0].command !== 'heif-dec {in} {out}' || filledKeys.length) throw new Error('Undo should put back what was there');
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

// A form that saves as you type keeps what the assistant filled in until
// the person saves it from the bar; and the form's own ✨ opens the helper
// with its two starters, the same helper each time.
func TestTheFormHelperOnAnAutosaveForm(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + formStub + `
var reqs = [];
global.fetch = function(url, o) {
  reqs.push({url: url, body: JSON.parse(o.body)});
  return Promise.resolve({ok: true, text: function() { return Promise.resolve('{"reply":"Set.","fields":{"name":"heic-magick"}}\n'); }});
};
autosave = true;
var host = node('div');
var formEl = components.fake_form({});
host.appendChild(formEl);
window.uiFormAssist(formEl);
var helper = find(host, 'ui-assist-helper');
if (!helper || formEl.style.display !== 'none') throw new Error('Get assistance should swap the helper in');
var starters = find(helper, 'ui-assist-choices');
if (!starters || starters.children.length !== 2) throw new Error('two starters should be offered');
fire(starters.children[0]);
setImmediate(function() { setImmediate(function() {
  if (reqs[0].url !== '/api/assist/form' || reqs[0].body.message !== 'Help me fill this in') throw new Error('the starter should go to the form help: ' + JSON.stringify(reqs[0]));
  if (saved) throw new Error('an autosave form saved what it filled on its own');
  fire(find(helper, 'ui-row-btn'));
  var bar = find(host, 'ui-assist-bar');
  if (texts(bar).join(' ').indexOf('not saved yet') < 0) throw new Error('the bar should say it is not saved: ' + texts(bar).join(' '));
  var save = bar.children.filter(function(c) { return c.children && String(c.children[0]) === 'Save what it filled in'; })[0];
  fire(save);
  if (!saved || saved.join(',') !== 'name') throw new Error('Save should save what it filled: ' + saved);
  window.uiFormAssist(formEl);
  var helpers = 0;
  (function walk(n) { if (n.className === 'ui-assist-helper') helpers++; (n.children || []).forEach(walk); })(host);
  if (helpers !== 1) throw new Error('a second Get assistance should reopen the same helper, found ' + helpers);
  console.log('OK');
}); });
`
	tmp := filepath.Join(t.TempDir(), "autosave.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the form helper on an autosave form:\n%s", out)
	}
}

// A form with an opening request (AssistAsk) starts its helper on it: sent
// the first time it opens, in place of the starters, and not again when it
// is opened a second time.
func TestTheFormHelperOpensOnItsRequest(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + formStub + `
var reqs = [];
global.fetch = function(url, o) {
  reqs.push({url: url, body: JSON.parse(o.body)});
  return Promise.resolve({ok: true, text: function() { return Promise.resolve('{"reply":"What is it about?"}\n'); }});
};
var host = node('div');
var formEl = components.fake_form({});
formEl.uiForm.cfg.assist_ask = 'Assist me with setting this up.';
host.appendChild(formEl);
window.uiFormAssist(formEl);
var helper = find(host, 'ui-assist-helper');
if (find(helper, 'ui-assist-choices')) throw new Error('an opening request takes the place of the starters');
setImmediate(function() { setImmediate(function() {
  if (reqs.length !== 1 || reqs[0].body.message !== 'Assist me with setting this up.') throw new Error('the opening request should go out as the helper opens: ' + JSON.stringify(reqs));
  if (!reqs[0].body.form || reqs[0].body.form.length !== 4) throw new Error('with the form: ' + JSON.stringify(reqs[0].body.form));
  fire(find(helper, 'ui-row-btn'));
  window.uiFormAssist(formEl);
  setImmediate(function() {
    if (reqs.length !== 1) throw new Error('opening it again should not ask again: ' + reqs.length);
    console.log('OK');
  });
}); });
`
	tmp := filepath.Join(t.TempDir(), "ask.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the form helper's opening request:\n%s", out)
	}
}

// formStub is a form panel as the helper sees one, and the helpers the
// scripts use.
const formStub = `
global.setTimeout = function(fn) { fn(); };
function fetchJSON() { return Promise.resolve({available: true}); }
var formVals = {steps: [{command: 'heif-dec {in} {out}'}], name: '', key: 'hunter2'};
var filledKeys = [], saved = null, autosave = false;
components.fake_form = function(cfg) {
  var f = node('div'); f.className = 'ui-form';
  f.uiForm = {
    cfg: {fields: [
      {field: 'name', label: 'Name', type: 'text', required: true},
      {field: 'steps', label: 'Steps', type: 'rows', required: true, columns: [{field: 'command', label: 'Command'}]},
      {field: 'key', label: 'Key', type: 'password'},
      {field: 'prefer', label: 'Prefer', type: 'toggle'},
      {field: 'id', type: 'hidden'},
    ]},
    autosave: autosave, loaded: true,
    values: function() { return JSON.parse(JSON.stringify(formVals)); },
    fill: function(v, changed) { formVals = JSON.parse(JSON.stringify(v)); filledKeys = filledKeys.concat(changed || []); },
    filled: function() { return filledKeys; },
    unmark: function() { filledKeys = []; },
    saveFilled: function(keys) { saved = keys.slice(); filledKeys = []; },
  };
  return f;
};
function find(n, cls) {
  if (n.className && (' ' + n.className + ' ').indexOf(' ' + cls + ' ') >= 0) return n;
  for (var i = 0; i < (n.children || []).length; i++) { var f = find(n.children[i], cls); if (f) return f; }
  return null;
}
function fire(n) { (n.__click || []).forEach(function(f) { f(); }); }
function texts(n, out) { out = out || []; if (typeof n === 'string') { out.push(n); return out; } if (n.textContent) out.push(n.textContent); if (n.innerHTML) out.push(n.innerHTML); (n.children || []).forEach(function(c) { texts(c, out); }); return out; }
`

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
	head := basics[at : at+4000]
	for _, want := range []string{"wrap.uiForm = {", "values: function()", "fill: function(vals, changed)", "saveFilled: function(keys)", "onSaved: null", "loaded: false", "new Event('change')"} {
		if !strings.Contains(head, want) {
			t.Errorf("form_panel's assist handle lacks %q", want)
		}
	}
	fill := head[strings.Index(head, "fill: function(vals, changed)"):strings.Index(head, "filled: function()")]
	if strings.Contains(fill, "save(") || !strings.Contains(fill, "render();") {
		t.Errorf("a fill redraws and does not save:\n%s", fill)
	}
	// What it filled is marked until the person touches the field, and a
	// submit that goes through tells the helper.
	for _, want := range []string{"if (filled[field] && fieldEls[field]", "markFilled();", "wrap.uiForm.onSaved(resp)", "wizardAt = i;", "appendAssistButton();", "(cfg.assist || stepsMode) || cfg.no_assist", "['✨ ' + (cfg.assist_label || 'Get assistance')]"} {
		if !strings.Contains(basics, want) {
			t.Errorf("form_panel lacks %q", want)
		}
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
  f.uiForm = {cfg: {fields: []}, loaded: true, values: function() { return {}; }, fill: function() {},
    filled: function() { return []; }, unmark: function() {}, saveFilled: function() {}};
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

// The opening request waits for the form's record: it goes with the values,
// and before the record is in, those are not the real ones.
func TestAssistPanelWaitsForTheRecord(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := readRuntimeFile(t, "06_assist_editor.js")
	script := assistStubDOM + src + formStub + `
var realTimeout = require('timers').setTimeout;
global.setTimeout = realTimeout;
var sent = null;
global.fetch = function(url, o) { sent = JSON.parse(o.body); return Promise.resolve({ok: true, text: function() { return Promise.resolve('{"reply":"ok"}\n'); }}); };
var made = null;
var inner = components.fake_form;
components.fake_form = function(cfg) { made = inner(cfg); made.uiForm.loaded = false; formVals = {name: ''}; return made; };
components.assist_panel({url: 'x', ask: 'help', work: {type: 'fake_form'}});
realTimeout(function() {
  if (sent) throw new Error('sent before the record was in');
  formVals = {name: 'the real one'};
  made.uiForm.loaded = true;
  realTimeout(function() {
    if (!sent || sent.values.name !== 'the real one') throw new Error('want the request with the loaded values: ' + JSON.stringify(sent));
    console.log('OK');
  }, 150);
}, 120);
`
	tmp := filepath.Join(t.TempDir(), "wait.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the panel waiting for the record:\n%s", out)
	}
}

// The conversation scrolls in its own box: the helper and the draft window
// have a height, not a minimum, so a long conversation does not grow the
// editor (seen: the whole editor scrolled instead of the chat).
func TestTheConversationScrollsInItsOwnBox(t *testing.T) {
	css := readRuntimeCSSForTest(t)
	rule := func(sel string) string {
		at := strings.Index(css, "\n"+sel+" {")
		if at < 0 {
			t.Fatalf("no rule for %s", sel)
		}
		return css[at : at+strings.Index(css[at:], "}")]
	}
	for _, sel := range []string{".ui-assist", ".ui-assist-helper-b"} {
		if r := rule(sel); !strings.Contains(r, "height: min(") || strings.Contains(r, "min-height: min(") {
			t.Errorf("%s should have a height, not a minimum:%s", sel, r)
		}
	}
	if r := rule(".ui-assist-log"); !strings.Contains(r, "overflow-y: auto") || !strings.Contains(r, "min-height: 0") {
		t.Errorf("the log should scroll inside its box:%s", r)
	}
}
