package ui

// Direct editing in a WorkbenchPanel (EditURL). The value of the feature is
// entirely in what it refuses to do: it must not open over a record that has
// no editable source, and it must not let a background refresh — a co-author
// write, a chat round ending — replace text somebody is in the middle of
// typing.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchEditMarshalsItsFields(t *testing.T) {
	raw, err := json.Marshal(WorkbenchPanel{
		ListURL: "docs", RecordURL: "doc?id={id}",
		EditURL: "body?id={id}", EditField: "markdown", EditLabel: "Edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"edit_url":"body?id={id}"`, `"edit_field":"markdown"`, `"edit_label":"Edit"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s from the wire form:\n%s", want, body)
		}
	}
	// A workbench that offers no direct editing carries none of it. These ride
	// on every workbench, and the runtime keys the toggle's existence off the
	// URL being present at all.
	plain, _ := json.Marshal(WorkbenchPanel{ListURL: "docs"})
	for _, unwanted := range []string{"edit_url", "edit_field", "edit_label"} {
		if strings.Contains(string(plain), unwanted) {
			t.Errorf("%q should be omitted when direct editing is off: %s", unwanted, plain)
		}
	}
}

// The three rules the runtime has to get right, checked against the real
// renderer under a stub DOM.
func TestWorkbenchEditorGuardsTheOpenDraft(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	src := readRuntimeFile(t, "70_misc.js")
	if !strings.Contains(src, "cfg.edit_url") {
		t.Fatal("the workbench renderer knows nothing about direct editing")
	}

	harness := `
function node(tag) {
  return {
    tagName: tag, value: '', textContent: '', className: '', disabled: false, hidden: false,
    style: {}, children: [], files: [],
    appendChild: function(c) { this.children.push(c); return c; },
    insertBefore: function(c) { this.children.unshift(c); return c; },
    removeChild: function() {}, remove: function() {},
    setAttribute: function(k, v) { this['attr_' + k] = v; },
    getAttribute: function(k) { return this['attr_' + k] === undefined ? null : this['attr_' + k]; },
    addEventListener: function(k, fn) { (this.on = this.on || {})[k] = fn; },
    querySelector: function() { return null; }, querySelectorAll: function() { return []; },
    focus: function() {}, contains: function() { return false; },
    classList: {add: function(){}, remove: function(){}, toggle: function(){}},
    set innerHTML(v) { this._html = v; this.children = []; },
    get innerHTML() { return this._html || ''; },
  };
}
global.document = {
  createElement: node, createTextNode: function(t) { return {text: t}; },
  addEventListener: function() {}, removeEventListener: function() {}, body: node('body'),
  querySelector: function() { return null; }, querySelectorAll: function() { return []; },
};
global.window = {addEventListener: function(k, fn) { (global.winOn = global.winOn || {})[k] = fn; }};
function el(tag, attrs, kids) {
  var n = node(tag);
  Object.keys(attrs || {}).forEach(function(k) {
    if (k === 'class') n.className = attrs[k];
    else if (k === 'text') n.textContent = attrs[k];
    else if (typeof attrs[k] === 'function') n[k] = attrs[k];
    else n.setAttribute(k, attrs[k]), n[k] = attrs[k];
  });
  (kids || []).forEach(function(k) { n.appendChild(k); });
  return n;
}
var components = {};
function mountComponent() {}
function makeDrawer(col, o) {
  return {mobileHdr: node('div'), backdrop: node('div'), mobileTitle: node('div'), closeDrawer: function(){}};
}
function uiRenderMarkdown(host, md) { host._md = md; }
function showToast(m) { global.toast = m; }

// The records the panel will fetch: an ARTICLE (editable source present) and
// a GUIDE (no source — the toggle must stay dead for it).
var records = {
  a1: {id: 'a1', html: '<p>rendered article</p>', markdown: 'the source text'},
  g1: {id: 'g1', html: '<p>rendered guide</p>'},
};
var posted = null;
function fetchJSON(url) {
  var id = (url.match(/id=([^&]+)/) || [])[1];
  if (url.indexOf('docs') === 0) return Promise.resolve([{id: 'a1', title: 'Article'}, {id: 'g1', title: 'Guide'}]);
  return Promise.resolve(records[id]);
}
global.fetch = function(url, opts) {
  posted = {url: url, body: JSON.parse((opts && opts.body) || '{}')};
  return Promise.resolve({ok: true, text: function() { return Promise.resolve(''); }});
};

` + src + `

var panel = components.workbench_panel({
  list_url: 'docs', record_url: 'doc?id={id}',
  edit_url: 'body?id={id}', edit_field: 'markdown',
  body_field: 'html', body_is_html: true,
  viewer_actions: [{label: 'Export', kind: 'download', url: 'x'}],
});

// The Edit toggle is the first button on the action bar.
var bar = panel.children.filter(function(c) { return c.className === 'ui-wb-col ui-wb-viewer'; })[0];
var editBtn = bar.children[0].children[0];
if (editBtn.textContent !== 'Edit') throw new Error('no Edit toggle: ' + editBtn.textContent);

function tick() { return new Promise(function(r) { setTimeout(r, 1); }); }

Promise.resolve()
  .then(function() {
    // Open the GUIDE. It carries no editable source, so the toggle stays off.
    panel.loadViewer('g1');
    return tick();
  })
  .then(function() {
    if (!editBtn.disabled) throw new Error('a guide has no editable source; the toggle must stay disabled');
    // Open the ARTICLE. Now it lights up.
    panel.loadViewer('a1');
    return tick();
  })
  .then(function() {
    if (editBtn.disabled) throw new Error('an article carries its source; the toggle should be live');
    editBtn.on.click();
    var ta = panel.editorTextarea();
    if (!ta || ta.value !== 'the source text') throw new Error('the editor did not open on the source: ' + (ta && ta.value));
    // Type, then let a background refresh land — the chat finished a round.
    ta.value = 'half-typed edit';
    panel.loadViewer('a1');
    return tick();
  })
  .then(function() {
    var ta = panel.editorTextarea();
    if (!ta || ta.value !== 'half-typed edit') throw new Error('a refresh wiped the open draft: ' + (ta && ta.value));
    // Saving posts the edited field, keyed by the record id.
    panel.editorSave();
    return tick();
  })
  .then(function() {
    if (!posted || posted.url !== 'body?id=a1') throw new Error('save went to ' + (posted && posted.url));
    if (posted.body.markdown !== 'half-typed edit') throw new Error('save sent ' + JSON.stringify(posted.body));
    // Switching records closes the editor rather than carrying the draft over.
    editBtn.on.click();
    panel.loadViewer('a1');
    return tick();
  })
  .then(function() {
    panel.loadViewer('g1');
    return tick();
  })
  .then(function() {
    if (panel.editorTextarea()) throw new Error('the editor survived a switch to another record');
    console.log('OK');
  })
  .catch(function(e) { console.log('FAIL ' + e.message); });
`
	tmp := filepath.Join(t.TempDir(), "wb.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("direct editing does not hold its contract:\n%s", out)
	}
}
