package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Add AI generator's picker, over templates of several kinds, opens on a
// switch of the kinds (in the server's order, each with its count) showing
// the first kind's templates alone; a kind is a tap away, not a scroll past
// the others. A picker of one kind is the plain list.
func TestThePickerSwitchesBetweenKinds(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	harness := `
function mk(tag, opts, kids) {
  opts = opts || {};
  var n = {tag: tag, cls: opts['class'] || '', text: opts.text || '', kids: [], on: {},
    classList: {toggle: function(c, on) { var s = n.cls.split(' ').filter(Boolean).filter(function(x){ return x !== c; }); if (on) s.push(c); n.cls = s.join(' '); }},
    appendChild: function(k) { this.kids.push(k); return k; },
    removeChild: function(k) { this.kids.splice(this.kids.indexOf(k), 1); },
    addEventListener: function(e, f) { this.on[e] = f; }};
  Object.defineProperty(n, 'firstChild', {get: function() { return n.kids[0]; }});
  (kids || []).forEach(function(k) { n.kids.push(k); });
  return n;
}
function texts(n, out) { out = out || []; if (n.text) out.push(n.text); n.kids.forEach(function(k){ texts(k, out); }); return out; }
function find(n, cls, out) { out = out || []; if ((' ' + n.cls + ' ').indexOf(' ' + cls + ' ') >= 0) out.push(n); n.kids.forEach(function(k){ find(k, cls, out); }); return out; }
var window = {uiEl: mk, UIClientActions: {}};
var rows;
function fetch() { return Promise.resolve({ok: true, text: function(){ return Promise.resolve(JSON.stringify(rows)); }}); }
var body;
window.uiOpenSimpleModal = function(o) { body = mk('div'); o.mount(body, {}); };
var action = ` + connectorAddAnotherAction + `;
function want(cond, why) { if (!cond) { console.log('FAIL ' + why); process.exitCode = 1; } }
rows = [{id: 'a', title: 'Gemini', category: 'Images'}, {id: 'b', title: 'ComfyUI', category: 'Images'},
  {id: 'c', title: 'ElevenLabs', category: 'Music'}, {id: 'd', title: 'Omni', category: 'Video'}];
action({action: {data: 'media', label: 'Add AI generator…'}});
setTimeout(function() {
  var segs = find(body, 'ui-seg');
  want(segs.map(function(s){ return s.text; }).join(',') === 'Images (2),Music (1),Video (1)', 'the kinds, in order, with counts: ' + segs.map(function(s){ return s.text; }));
  want(segs[0].cls.indexOf('active') >= 0, 'it opens on the first kind');
  var shown = function() { return texts(body).filter(function(t){ return ['Gemini','ComfyUI','ElevenLabs','Omni'].indexOf(t) >= 0; }).join(','); };
  want(shown() === 'Gemini,ComfyUI', 'only the first kind shows at first: ' + shown());
  segs[1].on.click();
  want(shown() === 'ElevenLabs' && segs[1].cls.indexOf('active') >= 0 && segs[0].cls.indexOf('active') < 0, 'a tap shows that kind alone: ' + shown());
  rows = [{id: 'm', title: 'Resend', category: 'Mail'}];
  action({value: 'mail.send'});
  setTimeout(function() {
    want(find(body, 'ui-seg').length === 0 && texts(body).indexOf('Resend') >= 0, 'one kind is the plain list');
    if (!process.exitCode) console.log('OK');
  }, 10);
}, 10);
`
	tmp := filepath.Join(t.TempDir(), "picker.js")
	if err := os.WriteFile(tmp, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the picker:\n%s", out)
	}
}
