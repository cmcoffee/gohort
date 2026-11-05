package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The chat's markdown renderer turns model output, other people's shared
// text and app records into HTML that goes into the page with innerHTML.
// Every anchor it makes must be exactly one: a quoted href with no tag in it,
// and nothing after that but target and rel. A link pass that rewrote inside
// another link's href made an anchor whose attribute closed early and carried
// an event handler of its own.
func TestTheMarkdownRendererMakesNoAnchorThatBreaksOut(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	md := boundedRuntime(t, readRuntimeFile(t, "00_prelude.js"), "  function mdToHTML(", "\n  }\n")
	script := "var window = {};\n" + md + `
var inputs = [
  '[t](https://a https://x/onmouseover=location="javascript:alert%281%29"+)',
  '[t](https://a<https://x/onmouseover=location="javascript:alert%281%29"//>)',
  '[t](https://a(https://x/onmouseover=location="javascript:alert%281%29"//))',
  'see https://x<a href="https://a onmouseover=alert(1) x">y</a>',
  '<https://a https://b/onmouseover=alert(1)>',
  '[x](//evil.example/path)', '[x](/\\evil.example)',
  '[ok](https://example.com/a) then https://b.example/c. and <https://c.example> and [rel](/p?q=1)',
];
var bad = 0;
inputs.forEach(function(x) {
  var out = mdToHTML(x);
  var tags = out.match(/<a\b[^>]*>/g) || [];
  tags.forEach(function(tag) {
    if (!/^<a href="[^"<>]*"( target="_blank" rel="noopener")?>$/.test(tag)) { bad++; console.log('BAD ' + tag + '  from ' + x); }
    if (/^<a href="\/[\/\\]/.test(tag)) { bad++; console.log('OFFSITE ' + tag); }
  });
});
var ok = mdToHTML(inputs[inputs.length - 1]);
['href="https://example.com/a"', 'href="https://b.example/c"', 'href="https://c.example"', 'href="/p?q=1"'].forEach(function(want) {
  if (ok.indexOf(want) < 0) { bad++; console.log('MISSING ' + want + ' in ' + ok); }
});
process.exit(bad ? 1 : 0);
`
	path := filepath.Join(t.TempDir(), "md.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", path).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// runJS runs a script under node and fails the test with its output when it
// exits non-zero.
func runJS(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	path := filepath.Join(t.TempDir(), "x.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", path).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// uiGoTo follows only the URL it checked. A javascript: URL checks to "#",
// which is the current page, and the same-page branch replace()d with the
// raw URL, running it.
func TestUIGoToNeverFollowsAScriptURL(t *testing.T) {
	prelude := readRuntimeFile(t, "00_prelude.js")
	safe := boundedRuntime(t, prelude, "  function safeURL(", "\n  }\n")
	goTo := boundedRuntime(t, prelude, "  window.uiGoTo = function(dest) {", "\n  };\n")
	runJS(t, `
var window = {}, went = [];
var location = {pathname: '/app/page', search: '?x=1', origin: 'http://h',
  replace: function(u) { went.push(u); }};
Object.defineProperty(location, 'href', {get: function() { return 'http://h/app/page?x=1'; }, set: function(u) { went.push(u); }});
var document = {createElement: function() {
  var a = {};
  Object.defineProperty(a, 'href', {set: function(v) { var u = new URL(v, 'http://h/app/page?x=1'); a.pathname = u.pathname; a.search = u.search; }});
  return a;
}};
`+safe+"\n"+goTo+`
['javascript:alert(1)', ' JaVaScRiPt:alert(1)', 'vbscript:x'].forEach(function(u) { window.uiGoTo(u); });
var bad = went.filter(function(u) { return /script:/i.test(u); });
if (bad.length) { console.log('followed ' + bad.join(', ')); process.exit(1); }
went = []; window.uiGoTo('/app/other');
if (went[0] !== '/app/other') { console.log('an ordinary path: ' + went); process.exit(1); }
`)
}

// The artifact pane's url mode frames a page with no sandbox: only a path on
// this origin, with no control character a browser would drop.
func TestTheArtifactPaneFramesOnlyThisOrigin(t *testing.T) {
	fn := boundedRuntime(t, readRuntimeFile(t, "00_prelude.js"), "    function sameOriginPath(u) {", "\n    }\n")
	runJS(t, "var location = {origin: 'http://h'};\n"+fn+`
var bad = ['//evil.example', '/\\evil.example', '/\t/evil.example', '/\n/evil.example', '/ /x', 'https://evil.example', 'javascript:1'].filter(sameOriginPath);
if (bad.length) { console.log('framed ' + JSON.stringify(bad)); process.exit(1); }
if (!sameOriginPath('/apps/notes/')) { console.log('refused a path on this origin'); process.exit(1); }
`)
}

// "Open in a new tab" never opens the artifact itself as a page of this
// origin: a blob URL takes the origin of the page that made it. It opens a
// page holding the artifact in a frame sandboxed as the pane's is.
func TestAnArtifactOpensInANewTabSandboxed(t *testing.T) {
	src := readRuntimeFile(t, "00_prelude.js")
	i := strings.Index(src, "popBtn.addEventListener('click'")
	if i < 0 {
		t.Fatal("the pop-out button's handler was not found")
	}
	body := src[i : i+strings.Index(src[i:], "\n    });\n")]
	if strings.Contains(body, "new Blob([state.html") {
		t.Error("the artifact's own HTML is opened as a page of this origin")
	}
	if !strings.Contains(body, `<iframe sandbox="allow-scripts allow-modals" srcdoc="`) {
		t.Error("the new tab does not hold the artifact in a sandboxed frame")
	}
}
