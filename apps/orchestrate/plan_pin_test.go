package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The plan checklist asks to stay pinned at the top of the thread while
// steps remain, keeps the step under way in view, and says how it ended.
// Runs the real renderer from the page assets under node, over a stand-in
// DOM; skips where node is not installed.
func TestThePlanPinsItselfWhileStepsRemain(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := os.ReadFile(filepath.Join("assets", "web_assets.html"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "window.uiRegisterBlockRenderer('orchestrate_plan'")
	end := strings.Index(s[start:], "\n    });\n")
	if start < 0 || end < 0 {
		t.Fatal("the orchestrate_plan renderer is gone")
	}
	harness := `
function node(tag, attrs) {
  var n = {tag: tag, kids: [], attrs: {}, textContent: '', set innerHTML(v) { this.kids = []; },
    appendChild: function(c) { this.kids.push(c); return c; },
    setAttribute: function(k, v) { this.attrs[k] = v; }, removeAttribute: function(k) { delete this.attrs[k]; }};
  if (attrs && attrs.class) n.cls = attrs.class;
  return n;
}
function el(tag, attrs, kids) { var n = node(tag, attrs); (kids || []).forEach(function(k) { n.textContent += k; }); return n; }
var fn;
var window = {uiRegisterBlockRenderer: function(name, f) { fn = f; }};
` + s[start:start+end] + "\n    });\n" + `
var fail = 0;
function check(label, cond) { if (!cond) { fail++; console.log('FAIL ' + label); } }
var b = fn({plan: [{title: 'a', status: 'done'}, {title: 'b', status: 'in_progress'}, {title: 'c', status: 'pending'}]});
var rows = b.body.kids;
check('steps remaining: live', b.wrap.attrs['data-ui-pin'] === 'live');
check('the heading counts: ' + b.wrap.kids[0].textContent, b.wrap.kids[0].textContent === '▸ Plan - 1 of 3 done');
check('the step under way is the one kept in view', 'data-ui-pin-focus' in rows[1].attrs && !('data-ui-pin-focus' in rows[2].attrs));
b.onUpdate({plan: [{title: 'a', status: 'done'}, {title: 'b', status: 'done'}, {title: 'c', status: 'pending'}]});
check('none under way: the next pending one is kept in view', 'data-ui-pin-focus' in b.body.kids[2].attrs);
b.onUpdate({plan: [{title: 'a', status: 'done'}, {title: 'b', status: 'done'}, {title: 'c', status: 'done'}]});
check('all done: done', b.wrap.attrs['data-ui-pin'] === 'done' && b.wrap.kids[0].textContent === '✓ Plan done - 3 of 3');
b.onUpdate({plan: [{title: 'a', status: 'done'}, {title: 'b', status: 'blocked'}]});
check('ended with one blocked says so: ' + b.wrap.kids[0].textContent,
  b.wrap.attrs['data-ui-pin'] === 'done' && b.wrap.kids[0].textContent === '▸ Plan ended - 1 of 2 done, 1 blocked');
b.onUpdate({plan: []});
check('no steps: nothing to pin', !('data-ui-pin' in b.wrap.attrs));
if (fail) process.exit(1);
console.log('ok');
`
	f := filepath.Join(t.TempDir(), "plan_pin.js")
	os.WriteFile(f, []byte(harness), 0o644)
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
