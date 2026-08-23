package orchestrate

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; the save-time JS check yields no verdict here")
	}
}

// TestHTMLScriptSyntaxCatchesRealErrors uses failures the Law Bird session
// actually hit — each one cost a full update/verify round-trip against a stale
// revision before the author learned what was wrong.
//
// NOT covered here: the session's `break` outside a loop. It is an early error
// only on newer V8; the node on a given host may exit 0 on it, and asserting
// otherwise would make this test a report on the local node version. The
// on-save page check is what catches that class — see appPageRuntimeErrors.
func TestHTMLScriptSyntaxCatchesRealErrors(t *testing.T) {
	requireNode(t)
	cases := []struct {
		name string
		html string
		want string // substring the report must name
	}{
		{
			name: "invalid assignment target",
			html: `<html><body><script>
var bird = {};
bird.x + 1 = 5;
</script></body></html>`,
			want: "left-hand side",
		},
		{
			name: "unterminated block",
			html: `<html><body><script>
function draw() {
  ctx.fillRect(0, 0, 10, 10);
</script></body></html>`,
			want: "line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probs, checked := htmlScriptSyntaxProblems(context.Background(), tc.html)
			if !checked {
				t.Skip("sandbox could not reach node; no verdict")
			}
			if len(probs) == 0 {
				t.Fatalf("expected a syntax problem for %s", tc.name)
			}
			joined := strings.ToLower(strings.Join(probs, " "))
			if !strings.Contains(joined, strings.ToLower(tc.want)) {
				t.Errorf("report %q should name %q", joined, tc.want)
			}
			if !strings.Contains(joined, "script block 1") {
				t.Errorf("report should say which block: %q", joined)
			}
		})
	}
}

// TestHTMLScriptSyntaxAcceptsValid — a working game must save clean. False
// accusations here would be worse than no check at all.
func TestHTMLScriptSyntaxAcceptsValid(t *testing.T) {
	requireNode(t)
	html := `<!DOCTYPE html><html><head><style>*{margin:0}</style></head><body>
<canvas id="g"></canvas>
<script>
var c = document.getElementById('g'), ctx = c.getContext('2d');
var bird = {x: 80, y: 300, w: 28, h: 28};
function loop() {
  ctx.clearRect(0, 0, 400, 600);
  ctx.fillRect(bird.x, bird.y, bird.w, bird.h);
  requestAnimationFrame(loop);
}
document.addEventListener('keydown', function(e) { if (e.code === 'Space') bird.y -= 40; });
loop();
</script></body></html>`
	probs, checked := htmlScriptSyntaxProblems(context.Background(), html)
	if !checked {
		t.Skip("sandbox could not reach node; no verdict")
	}
	if len(probs) != 0 {
		t.Fatalf("valid game reported problems: %v", probs)
	}
}

// TestHTMLScriptSyntaxSkipsNonJS — a JSON payload or an external reference is
// not JavaScript, and feeding either to a JS parser invents errors.
func TestHTMLScriptSyntaxSkipsNonJS(t *testing.T) {
	requireNode(t)
	html := `<html><body>
<script type="application/json">{"level": 1, "not": "javascript"}</script>
<script src="/some/where.js"></script>
<script>var ok = 1;</script>
</body></html>`
	probs, checked := htmlScriptSyntaxProblems(context.Background(), html)
	if !checked {
		t.Skip("sandbox could not reach node; no verdict")
	}
	if len(probs) != 0 {
		t.Fatalf("non-JS blocks should be skipped, got: %v", probs)
	}
}

// TestHTMLScriptSyntaxNoScripts — a markup-only section has nothing to parse,
// which is "no verdict", not "passed".
func TestHTMLScriptSyntaxNoScripts(t *testing.T) {
	if _, checked := htmlScriptSyntaxProblems(context.Background(), "<div>just markup</div>"); checked {
		t.Error("a section with no inline script yields no verdict")
	}
}

// TestAppHTMLSectionScripts pulls html blobs out of the authoring array,
// including a section whose kind is only implied.
func TestAppHTMLSectionScripts(t *testing.T) {
	got := appHTMLSectionScripts([]any{
		map[string]any{"kind": "html", "html": "<p>one</p>"},
		map[string]any{"kind": "table", "columns": []any{}},
		map[string]any{"html": "<p>two</p>"}, // kind inferred
		map[string]any{"kind": "html"},       // empty, skipped
	})
	if len(got) != 2 || got[0] != "<p>one</p>" || got[1] != "<p>two</p>" {
		t.Fatalf("collected = %v", got)
	}
}

// TestNodeCheckDetail keeps the part an author can act on and drops the temp
// path and stack that mean nothing to someone editing an html section.
func TestNodeCheckDetail(t *testing.T) {
	raw := `/tmp/appjs-123/block1.js:4
  break;
  ^^^^^
SyntaxError: Illegal break statement
    at wrapSafe (node:internal/modules/cjs/loader:1234:18)
Node.js v20.11.0`
	got := nodeCheckDetail(raw)
	if strings.Contains(got, "/tmp/appjs-") {
		t.Errorf("temp path should be dropped: %q", got)
	}
	if strings.Contains(got, "at wrapSafe") || strings.Contains(got, "Node.js v") {
		t.Errorf("stack/banner should be dropped: %q", got)
	}
	if !strings.Contains(got, "line 4") {
		t.Errorf("line number is the one navigational fact worth keeping: %q", got)
	}
	if !strings.Contains(got, "Illegal break statement") {
		t.Errorf("the error itself must survive: %q", got)
	}
}
