package orchestrate

import (
	"strings"
	"testing"
)

// TestReplaceFunctionSpliceKeepsTheRestByteForByte — the property that makes
// replace_function worth having: the author supplies ONE function and nothing
// else in the document moves. The old text is never reproduced, so it can
// never be reproduced wrong.
func TestReplaceFunctionSpliceKeepsTheRestByteForByte(t *testing.T) {
	html := mapStr(gameSections()[0], "html")
	start, end, err := jsFunctionSpan(html, "draw")
	if err != nil {
		t.Fatalf("locate draw: %v", err)
	}
	if got := html[start:end]; got != "function draw() { ctx.fillRect(bird.x, bird.y, bird.w, bird.h); }" {
		t.Fatalf("span covered the wrong bytes: %q", got)
	}
	replacement := "function draw() {\n  ctx.save();\n  ctx.fillRect(bird.x, bird.y, bird.w, bird.h);\n  ctx.restore();\n}"
	next := html[:start] + replacement + html[end:]

	if !strings.Contains(next, "ctx.restore();") {
		t.Error("replacement did not land")
	}
	// Everything the author didn't name is untouched.
	for _, keep := range []string{"var GAP = 155;", "function spawn() { pipes.push({x: 400, gap: GAP}); }", "<canvas id=\"g\"></canvas>"} {
		if !strings.Contains(next, keep) {
			t.Errorf("replacement disturbed unrelated content: %q is gone", keep)
		}
	}
	if strings.Count(next, "function draw()") != 1 {
		t.Error("the old definition was not removed")
	}
	// And the swap didn't orphan anything.
	if broke := jsNewDanglingCalls(html, next); len(broke) != 0 {
		t.Errorf("a clean swap should break nothing, got %v", broke)
	}
}

// TestReplaceFunctionRejectsBodyOnlyReplacement — handing over just the body
// would delete the definition line and leave every call site dangling. Say so
// in terms of what the author did, not what it caused.
func TestReplaceFunctionRejectsBodyOnlyReplacement(t *testing.T) {
	if definesFunction("ctx.fillRect(0,0,1,1);", "draw") {
		t.Error("a bare body must not count as defining the function")
	}
	if !definesFunction("function draw() { ctx.fillRect(0,0,1,1); }", "draw") {
		t.Error("a whole function should count as defining itself")
	}
	if !definesFunction("const draw = () => { go(); };", "draw") {
		t.Error("an arrow binding should count as defining the function")
	}
	// A replacement that defines a DIFFERENT function is the rename mistake —
	// it parses, and every existing call site dies.
	if definesFunction("function drawBird() { }", "draw") {
		t.Error("a differently-named function must not satisfy the check")
	}
}

func TestIsJSFunctionName(t *testing.T) {
	ok := []string{"draw", "_x", "$el", "drawBird2", "GameLoop"}
	bad := []string{"", "2fast", "draw()", "ctx.draw", "draw bird", "function draw"}
	for _, s := range ok {
		if !isJSFunctionName(s) {
			t.Errorf("%q should be a valid function name", s)
		}
	}
	for _, s := range bad {
		if isJSFunctionName(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}
