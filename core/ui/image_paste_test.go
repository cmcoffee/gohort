package ui

// uiImagePaste: a screenshot pasted into a markdown textarea lands as its own
// paragraph where the cursor was, a placeholder holding the spot while it
// uploads; a failed upload takes the placeholder away and says so.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImagePasteLandsAtTheCursor(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "00_prelude.js")
	i := strings.Index(src, "  var imagePasteSeq = 0;")
	j := strings.Index(src, "  window.uiImagePaste = uiImagePaste;")
	if i < 0 || j < 0 {
		t.Fatal("uiImagePaste is gone")
	}
	harness := `
var alerts = [], respond = null;
global.window = {uiAlert: function(m) { alerts.push(m); }};
global.Event = function(type) { this.type = type; };
global.FormData = function() { this.append = function() {}; };
global.fetch = function() { return new Promise(function(res) { respond = res; }); };
function ta() {
  var on = {};
  return {value: '', selectionStart: 0, selectionEnd: 0, classList: {add: function(){}, remove: function(){}},
    addEventListener: function(k, f) { on[k] = f; }, dispatchEvent: function() {}, focus: function() {}, on: on};
}
` + src[i:j] + `
function paste(t) {
  var prevented = false;
  t.on.paste({preventDefault: function() { prevented = true; }, clipboardData: {items: [
    {kind: 'file', type: 'image/png', getAsFile: function() { return {name: 'image.png', type: 'image/png'}; }}]}});
  return prevented;
}
(async function() {
  var t = ta();
  t.value = 'Before the picture.After it.';
  t.selectionStart = t.selectionEnd = 'Before the picture.'.length;
  uiImagePaste(t, 'images?id=g1');
  if (!paste(t)) throw new Error('a pasted image should not also paste as text');
  if (t.value !== 'Before the picture.\n\n![Uploading image 1…]()\n\nAfter it.') throw new Error('placeholder not its own paragraph: ' + JSON.stringify(t.value));
  respond({ok: true, json: function() { return Promise.resolve({markdown: '![Screenshot](/app/img?id=x)'}); }});
  await new Promise(function(r) { setTimeout(r, 0); });
  if (t.value !== 'Before the picture.\n\n![Screenshot](/app/img?id=x)\n\nAfter it.') throw new Error('placeholder not replaced: ' + JSON.stringify(t.value));

  var u = ta();
  u.value = 'Text';
  u.selectionStart = u.selectionEnd = 4;
  uiImagePaste(u, 'images?id=g1');
  paste(u);
  respond({ok: false, status: 415, text: function() { return Promise.resolve('use a PNG, JPEG, GIF or WebP image'); }});
  await new Promise(function(r) { setTimeout(r, 0); });
  if (u.value.indexOf('Uploading') >= 0) throw new Error('a failed upload left its placeholder: ' + JSON.stringify(u.value));
  if (!alerts.length || alerts[0].indexOf('PNG') < 0) throw new Error('a failed upload should say why: ' + JSON.stringify(alerts));
  console.log('OK');
})().catch(function(e) { console.log(String(e)); process.exit(1); });
`
	tmp := filepath.Join(t.TempDir(), "paste.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("uiImagePaste does not hold:\n%s", out)
	}
}
