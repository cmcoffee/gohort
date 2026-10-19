package extensions

// The "Replies may ask for" panel, driven in node: every exception declared is
// listed, one can be added from the form under them, and one removed from its
// row. The panel once showed only the step being edited, so what was already
// allowed could not be seen.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stepsPanelHarness = `
function node(tag) {
  return {
    tagName: tag, value: '', textContent: '', className: '', disabled: false, checked: false, type: '',
    style: {}, children: [],
    appendChild: function(c) { this.children.push(c); return c; },
    addEventListener: function(k, fn) { (this['__' + k] = this['__' + k] || []).push(fn); },
    set innerHTML(v) { this.children = []; }, get innerHTML() { return ''; },
  };
}
global.document = { createElement: node, createTextNode: function(t) { return {text: t, children: []}; } };
global.window = { uiRenderScopePills: function() {}, uiAlert: function(m) { throw new Error('alert: ' + m); } };
var state = {actions: ['create_post', 'verify_post', 'get_feed'], steps: [{action: '*', follow: 'verify_post'}], note: 'n'};
var posted = [];
global.fetch = function(url, opts) {
  if (opts && opts.method === 'POST') {
    var b = JSON.parse(opts.body);
    if (url.indexOf('section=steps') >= 0) {
      posted.push(b);
      state.steps = state.steps.filter(function(s) { return !(s.action === b.action && s.follow === b.follow); });
      if (b.on) state.steps.push({action: b.action, follow: b.follow});
    }
    return Promise.resolve({ok: true, text: function() { return Promise.resolve(''); }});
  }
  var body = url.indexOf('section=steps') >= 0 ? JSON.parse(JSON.stringify(state)) : {mode: 'all', agents: []};
  return Promise.resolve({ok: true, json: function() { return Promise.resolve(body); }});
};
var modal = node('div');
window.uiOpenSimpleModal = function(o) { o.mount(modal); };
function walk(n, f) { f(n); (n.children || []).forEach(function(c) { walk(c, f); }); }
function text(n) { var s = ''; walk(n, function(x) { s += (x.text || '') + (x.children && x.children.length ? '' : (x.textContent || '')) + ' '; }); return s; }
function find(f) { var hit = null; walk(modal, function(x) { if (!hit && f(x)) hit = x; }); return hit; }
function fire(n, k) { (n['__' + k] || []).forEach(function(f) { f(); }); }
function tick() { return new Promise(function(r) { setTimeout(r, 5); }); }
`

func TestTheStepsPanelListsEveryExceptionAndAddsMore(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := stepsPanelHarness + "var open = " + toolSecurityJS() + `;
open({record: {name: 'social', mode: 'toolbox'}});
(async function() {
  await tick();
  if (text(modal).indexOf('verify_post may be asked for by every action') < 0) throw new Error('the declared exception is not listed: ' + text(modal));
  // Add another: get_feed may be asked for by create_post's reply.
  var sel = find(function(x) { return x.tagName === 'select'; });
  sel.value = 'get_feed'; fire(sel, 'change');
  var box = null;
  walk(modal, function(x) {
    if (x.tagName === 'label' && x.children.length === 2 && x.children[1].text === 'create_post') box = x.children[0];
  });
  if (!box) throw new Error('no checkbox for create_post');
  box.checked = true;
  fire(find(function(x) { return x.tagName === 'button' && x.textContent === 'Add'; }), 'click');
  await tick(); await tick();
  if (posted.length !== 1 || posted[0].action !== 'create_post' || posted[0].follow !== 'get_feed' || !posted[0].on) throw new Error('add posted ' + JSON.stringify(posted));
  var t = text(modal);
  if (t.indexOf('verify_post may be asked for by every action') < 0 || t.indexOf('get_feed may be asked for by create_post') < 0) throw new Error('both exceptions should be listed: ' + t);
  // Remove the first from its row.
  var rm = null;
  walk(modal, function(x) {
    if (x.children && x.children.length >= 3 && (x.children[0].textContent || '').indexOf('verify_post may be asked') === 0) rm = x.children[2];
  });
  fire(rm, 'click');
  await tick(); await tick();
  var last = posted[posted.length - 1];
  if (last.action !== '*' || last.follow !== 'verify_post' || last.on) throw new Error('remove posted ' + JSON.stringify(last));
  if (text(modal).indexOf('verify_post may be asked for') >= 0) throw new Error('the removed exception is still listed');
  console.log('OK');
})().catch(function(e) { console.log('FAIL ' + e.message); process.exit(1); });
`
	tmp := filepath.Join(t.TempDir(), "panel.js")
	if err := os.WriteFile(tmp, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the panel:\n%s", out)
	}
}
