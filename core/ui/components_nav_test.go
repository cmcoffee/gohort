package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A ModalButton with Items is a menu of dialogs: each item reaches the page
// with its own dialog's body, and the menu opens the one chosen.
//
// Driven rather than read: the real components.modal_button, run over a stub
// DOM, with the dialog it would open recorded instead of drawn.
func TestAModalButtonMenuOpensTheDialogChosen(t *testing.T) {
	raw, err := json.Marshal(ModalButton{Label: "New", Variant: "primary", Items: []ModalButton{
		{Label: "Empty", Title: "Start empty", Body: FormPanel{SubmitLabel: "Create"}},
		{Label: "Upload", Subtitle: "A file of your own", Width: "600px", Body: FormPanel{SubmitLabel: "Upload"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Items []struct {
			Label string          `json:"label"`
			Body  json.RawMessage `json:"body"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Items) != 2 {
		t.Fatalf("the items did not reach the page: %s", raw)
	}
	if !strings.Contains(string(cfg.Items[1].Body), `"submit_label":"Upload"`) {
		t.Fatalf("an item lost its dialog's body: %s", raw)
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	basics := readRuntimeFile(t, "10_basics.js")
	from := strings.Index(basics, "  components.modal_button = function(cfg, ctx) {")
	to := strings.Index(basics, "  components.json_view = function(cfg, ctx) {")
	if from < 0 || to < from {
		t.Fatal("could not find components.modal_button")
	}
	single, _ := json.Marshal(ModalButton{Label: "New", Items: []ModalButton{{Label: "Empty", Title: "Start empty", Body: FormPanel{SubmitLabel: "Create"}}}})
	script := `
function node(tag) {
  var n = {
    tagName: String(tag).toUpperCase(), textContent: '', className: '', style: {}, attrs: {}, children: [], on: {},
    appendChild: function(c) { if (c.parentNode) c.parentNode.removeChild(c); this.children.push(c); c.parentNode = this; return c; },
    removeChild: function(c) { var i = this.children.indexOf(c); if (i >= 0) this.children.splice(i, 1); c.parentNode = null; return c; },
    setAttribute: function(k, v) { this.attrs[k] = String(v); },
    addEventListener: function(k, fn) { this.on[k] = fn; },
    contains: function(x) { return x === this || this.children.some(function(c) { return c.contains && c.contains(x); }); },
  };
  return n;
}
var outside = null;
global.document = {
  createElement: node, body: node('body'),
  addEventListener: function(k, fn) { outside = fn; }, removeEventListener: function() { outside = null; },
};
function el(tag, opts, kids) {
  var n = node(tag);
  for (var k in (opts || {})) { if (k === 'class') n.className = opts[k]; else n.setAttribute(k, opts[k]); }
  (kids || []).forEach(function(c) { n.textContent += c; });
  return n;
}
var opened = [];
function openComponentDialog(o) { opened.push(o); }
var components = {};
global.window = {uiAnchorMenu: function(toggle, menu) {
  document.body.appendChild(menu);
  return {open: function() { menu.shown = true; }, close: function() { menu.shown = false; }};
}};
` + basics[from:to] + `
var row = components.modal_button(` + string(raw) + `, {id: 'r1'});
var toggle = row.children[0];
if (toggle.textContent !== 'New ▾' || !/primary/.test(toggle.className)) throw new Error('the menu button reads ' + toggle.textContent + ' (' + toggle.className + ')');
if (document.body.children.length) throw new Error('a menu was left on the page before it was opened');
toggle.on.click({stopPropagation: function() {}});
var menu = document.body.children[0];
var labels = menu.children.map(function(b) { return b.textContent; }).join(',');
if (!menu.shown || labels !== 'Empty,Upload') throw new Error('the open menu lists ' + labels);
menu.children[1].on.click();
if (opened.length !== 1) throw new Error('choosing an item opened ' + opened.length + ' dialogs');
var o = opened[0];
if (o.title !== 'Upload' || o.subtitle !== 'A file of your own' || o.width !== '600px' || o.component.submit_label !== 'Upload' || o.ctx.id !== 'r1')
  throw new Error('the wrong dialog opened: ' + JSON.stringify(o));
if (document.body.children.length) throw new Error('the menu stayed on the page after a choice');
// A click elsewhere closes it, and takes it off the page.
toggle.on.click({stopPropagation: function() {}});
outside({target: document.body});
if (document.body.children.length) throw new Error('a click elsewhere left the menu open');

// A menu of one is that item, opened by the button itself.
opened = [];
row = components.modal_button(` + string(single) + `, {});
row.children[0].on.click();
if (row.children[0].textContent !== 'New' || opened.length !== 1 || opened[0].title !== 'Start empty' || opened[0].component.submit_label !== 'Create')
  throw new Error('a menu of one did not open its item: ' + row.children[0].textContent + ' ' + JSON.stringify(opened));
console.log('OK');
`
	tmp := filepath.Join(t.TempDir(), "modal_menu.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the menu does not open the dialog chosen:\n%s", out)
	}
}
