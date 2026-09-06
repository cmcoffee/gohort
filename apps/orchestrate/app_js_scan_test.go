package orchestrate

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// wrapScript puts a fragment where the scanners look for it.
func wrapScript(js string) string { return "<html><body><script>\n" + js + "\n</script></body></html>" }

func TestJSMaskLiteralsHidesDelimiters(t *testing.T) {
	src := "var a = \"{ not code }\";\n" +
		"// } comment brace\n" +
		"/* } block brace */\n" +
		"var r = /[}]/g;\n" +
		"function real() { return 1; }\n"
	masked := jsMaskLiterals(src)
	if len(masked) != len(src) {
		t.Fatalf("mask changed length: %d != %d", len(masked), len(src))
	}
	if strings.Count(masked, "}") != 1 {
		t.Errorf("expected exactly one code brace to survive masking, got %d\n%s", strings.Count(masked, "}"), masked)
	}
	if !strings.Contains(masked, "function real() { return 1; }") {
		t.Errorf("real code was masked:\n%s", masked)
	}
	if strings.Contains(masked, "comment brace") || strings.Contains(masked, "not code") {
		t.Errorf("literal content survived masking:\n%s", masked)
	}
}

func TestJSMaskLiteralsTemplateAndDivision(t *testing.T) {
	// A division must NOT be read as a regex start, or everything after it
	// gets masked away and the scan goes blind.
	src := "var half = total / 2;\nfunction after() { return 7; }\n"
	if masked := jsMaskLiterals(src); !strings.Contains(masked, "function after()") {
		t.Errorf("division swallowed the rest of the file:\n%s", masked)
	}
	tmpl := "var s = " + string('`') + "a } ${x} b" + string('`') + ";\nfunction later() {}\n"
	masked := jsMaskLiterals(tmpl)
	if strings.Contains(masked, "}") != true {
		t.Fatalf("expected the real function braces to remain")
	}
	if !strings.Contains(masked, "function later()") {
		t.Errorf("template literal swallowed following code:\n%s", masked)
	}
}

func TestJSFunctionSpanForms(t *testing.T) {
	tests := []struct {
		name string
		src  string
		fn   string
		want string
	}{
		{
			name: "declaration",
			src:  "var x=1;\nfunction drawBird(a, b) {\n  if (a) { b(); }\n}\nvar y=2;\n",
			fn:   "drawBird",
			want: "function drawBird(a, b) {\n  if (a) { b(); }\n}",
		},
		{
			name: "const arrow",
			src:  "const drawCar = (c) => {\n  c.fill();\n};\nvar z=3;\n",
			fn:   "drawCar",
			want: "const drawCar = (c) => {\n  c.fill();\n};",
		},
		{
			name: "const function expression",
			src:  "const tick = function () { return 1; };\n",
			fn:   "tick",
			want: "const tick = function () { return 1; };",
		},
		{
			name: "bare assignment",
			src:  "window.ok=1;\nendGame = function () { stop(); };\n",
			fn:   "endGame",
			want: "endGame = function () { stop(); };",
		},
		{
			name: "async declaration",
			src:  "async function loadIt() { await go(); }\n",
			fn:   "loadIt",
			want: "async function loadIt() { await go(); }",
		},
		{
			name: "braces inside strings do not end it",
			src:  "function talk() {\n  say(\"}\");\n  say('}');\n}\nfunction next(){}\n",
			fn:   "talk",
			want: "function talk() {\n  say(\"}\");\n  say('}');\n}",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := jsFunctionSpan(tc.src, tc.fn)
			if err != nil {
				t.Fatalf("span: %v", err)
			}
			if got := tc.src[start:end]; got != tc.want {
				t.Errorf("span mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestJSFunctionSpanRefusesAmbiguityAndAbsence(t *testing.T) {
	dup := "function go(){1;}\nfunction go(){2;}\n"
	if _, _, err := jsFunctionSpan(dup, "go"); err == nil {
		t.Error("two definitions of the same name should be refused, not guessed at")
	}
	src := "function drawBird(){}\nfunction drawCar(){}\n"
	_, _, err := jsFunctionSpan(src, "drawTree")
	if err == nil {
		t.Fatal("expected a refusal for an unknown function")
	}
	// The refusal has to be actionable — an author that guessed the name needs
	// the real ones, not another round trip.
	if !strings.Contains(err.Error(), "drawBird") || !strings.Contains(err.Error(), "drawCar") {
		t.Errorf("refusal should list what the section defines, got: %v", err)
	}
}

func TestJSDanglingCallsFindsTheWipe(t *testing.T) {
	// The shape that motivated all of this: the loop survives, the drawing
	// functions it calls are gone, and it all parses perfectly.
	src := wrapScript("function gameLoop(){ drawBackground(); drawBird(); requestAnimationFrame(gameLoop); }")
	got := jsDanglingCalls(src)
	want := map[string]bool{"drawBackground": true, "drawBird": true}
	if len(got) != 2 {
		t.Fatalf("expected the two deleted functions, got %v", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected dangling name %q (from %v)", n, got)
		}
	}
}

func TestJSDanglingCallsStaysQuietOnHealthyCode(t *testing.T) {
	src := wrapScript(`
const canvas = document.getElementById('c');
const ctx = canvas.getContext('2d');
let frame = 0;
function helper(cb) { cb(frame); }
const draw = (n) => { ctx.fillRect(0, 0, n, n); };
function loop() {
  helper(draw);
  setTimeout(loop, 16);
  console.log(Math.max(1, 2), JSON.stringify({a: 1}));
  localStorage.setItem('k', String(frame));
}
window.addEventListener('load', loop);
`)
	if got := jsDanglingCalls(src); len(got) != 0 {
		t.Errorf("healthy code should produce no dangling names, got %v", got)
	}
}

// TestJSDanglingCallsQuietOnRealisticGame — the check gets to accuse an author
// of deleting working code, so a false positive is expensive. Exercise the
// constructs a canvas game actually uses and require silence.
func TestJSDanglingCallsQuietOnRealisticGame(t *testing.T) {
	src := wrapScript(`
const canvas = document.getElementById('game'), ctx = canvas.getContext('2d');
const W = canvas.width, H = canvas.height;
let audio = null, animId = null, running = false;
const sprites = { bird: new Image(), pipe: new Image() };

class Pipe {
  constructor(x) { this.x = x; this.gap = 150; }
  update(dt) { this.x -= dt * 0.2; }
  get right() { return this.x + 50; }
}
const pipes = [];

const state = {
  score: 0,
  reset() { this.score = 0; pipes.length = 0; },
  bump(n) { this.score += n; },
};

function initAudio() {
  try { audio = new (window.AudioContext || window.webkitAudioContext)(); }
  catch (e) { console.warn('no audio', e); }
}

function roundRect(x, y, w, h, r) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.closePath();
}

const drawBird = (y) => {
  const grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, '#fff');
  ctx.save(); ctx.translate(80, y); roundRect(-9, -2, 18, 15, 3); ctx.fill(); ctx.restore();
};

function draw(t) {
  ctx.clearRect(0, 0, W, H);
  drawBird(Math.sin(t / 200) * 4 + H / 2);
  for (const p of pipes) { p.update(16); }
  const { score } = state;
  ctx.fillText(String(score), 10, 20);
}

function loop(t) { draw(t); animId = requestAnimationFrame(loop); }

function flap() { if (!running) { start(); } }
function start() { state.reset(); initAudio(); running = true; requestAnimationFrame(loop); }

canvas.addEventListener('click', flap);
document.addEventListener('keydown', function (e) { if (e.code === 'Space') flap(); });
[1, 2, 3].forEach((n) => pipes.push(new Pipe(n * 200)));
(function boot() { localStorage.getItem('best'); })();
`)
	if got := jsDanglingCalls(src); len(got) != 0 {
		t.Errorf("realistic game code should produce no dangling names, got %v", got)
	}
}

func TestJSNewDanglingCallsIsADiff(t *testing.T) {
	// A name that was already dangling before the edit must not be blamed on
	// the edit — that is the property the whole check leans on.
	before := wrapScript("function loop(){ missing(); draw(); }\nfunction draw(){}")
	after := wrapScript("function loop(){ missing(); draw(); }")
	got := jsNewDanglingCalls(before, after)
	if len(got) != 1 || got[0] != "draw" {
		t.Fatalf("expected only the newly broken name, got %v", got)
	}
	same := jsNewDanglingCalls(before, before)
	if len(same) != 0 {
		t.Errorf("an unchanged document broke nothing, got %v", same)
	}
}

func TestJSDefinedFunctionsCoversBindings(t *testing.T) {
	src := wrapScript("function a(){}\nconst b = () => {};\nvar c = function(){};\nconst notAFunc = 42;\n")
	got := strings.Join(jsDefinedFunctions(src), ",")
	if got != "a,b,c" {
		t.Errorf("defined functions = %q, want \"a,b,c\"", got)
	}
}

func TestAppRewriteRiskCatchesTheWipe(t *testing.T) {
	prior := wrapScript("function gameLoop(){ drawBackground(); drawBird(); }\n" +
		"function drawBackground(){ /* " + strings.Repeat("x", 900) + " */ }\n" +
		"function drawBird(){ /* " + strings.Repeat("y", 900) + " */ }\n")
	next := wrapScript("function gameLoop(){ drawBackground(); drawBird(); }")

	risk := appRewriteRisk(prior, next)
	if risk == "" {
		t.Fatal("a document that deleted the functions it still calls must be refused")
	}
	for _, want := range []string{"drawBackground", "drawBird", "replace_function", "confirm_rewrite"} {
		if !strings.Contains(risk, want) {
			t.Errorf("refusal should mention %q:\n%s", want, risk)
		}
	}
}

func TestAppRewriteRiskAllowsHonestEdits(t *testing.T) {
	body := "function gameLoop(){ drawBird(); }\nfunction drawBird(){ /* " + strings.Repeat("z", 2000) + " */ }\n"
	prior := wrapScript(body)
	// Same functions, more code — the ordinary "polish the graphics" update.
	next := wrapScript(body + "function drawStar(){ /* new */ }\n")
	if risk := appRewriteRisk(prior, next); risk != "" {
		t.Errorf("a growing, complete document should save cleanly:\n%s", risk)
	}
	// A small html section is exempt: little to lose, and halving one is normal.
	if risk := appRewriteRisk(wrapScript("function a(){}"), wrapScript("")); risk != "" {
		t.Errorf("small sections should not be guarded:\n%s", risk)
	}
}

func TestAppRewriteRiskCatchesSharpShrinkAlone(t *testing.T) {
	// No dangling calls at all — the new document is internally consistent, it
	// is simply a third of the app. Size alone has to carry this one.
	prior := wrapScript("function a(){ /* " + strings.Repeat("q", 3000) + " */ }\nfunction b(){ a(); }\n")
	next := wrapScript("function b(){ }\n")
	risk := appRewriteRisk(prior, next)
	if risk == "" {
		t.Fatal("a document that lost two thirds of its size should be refused")
	}
	if !strings.Contains(risk, "Functions present before and missing now") {
		t.Errorf("refusal should name the dropped functions:\n%s", risk)
	}
}

// Undefined-identifier guard for the dashboard's inline JavaScript.
//
// `node --check` parses. It cannot see a reference to a variable that no longer
// exists, because that is a RUNTIME error — and the failure surfaces as a
// feature that silently does nothing, or a "Save failed: Can't find variable"
// alert, days later and only if a human happens to exercise that path.
//
// Both of this file's recent breakages were exactly that shape: a block of UI
// was removed and one reference to its state survived in a handler further
// down. The syntax check was green each time.
//
// So: collect every binding the script introduces, collect every identifier it
// USES as a value, and report the difference. Deliberately whole-file rather
// than per-scope — a variable declared in one handler and read in another is
// also a bug, and modelling JavaScript scope properly here would trade a real
// guard for a fussy one nobody trusts.
//
// The bar this has to clear is not "finds everything" but "never cries wolf".
// A test that reports a name a human then has to prove innocent gets muted, and
// a muted test is worse than none — so anything ambiguous is treated as
// declared, and the allowlist below is generous on purpose.

var (
	// Bindings. Each is matched against literal-masked source, so a keyword
	// inside a string or comment introduces nothing.
	jsDeclKeywordRE = regexp.MustCompile(`(^|[^\w$.])(?:var|let|const)\s`)
	jsDeclFuncRE    = regexp.MustCompile(`\bfunction\s*\*?\s*([A-Za-z_$][\w$]*)`)
	jsDeclClassRE   = regexp.MustCompile(`\bclass\s+([A-Za-z_$][\w$]*)`)
	jsDeclCatchRE   = regexp.MustCompile(`\bcatch\s*\(\s*([A-Za-z_$][\w$]*)`)
	jsDeclForRE     = regexp.MustCompile(`\bfor\s*\(\s*(?:var|let|const)?\s*([A-Za-z_$][\w$]*)`)
	jsDeclParamsRE  = regexp.MustCompile(`(?:function\s*\*?\s*[\w$]*\s*|\b)\(([^()]*)\)\s*(?:\{|=>)`)
	jsDeclArrow1RE  = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*=>`)
	// A method shorthand — `{ draw(ctx) {`, `get width() {` — declares a name
	// as surely as `function draw` does.
	jsDeclMethodRE = regexp.MustCompile(`(?m)(?:^|[,{;]|\bget\b|\bset\b|\bstatic\b|\basync\b)\s*([A-Za-z_$][\w$]*)\s*\([^()]*\)\s*\{`)
	// A USE: an identifier read as a value — called, indexed, or reached into.
	// Not preceded by a dot (that is a property, not a free name) and not
	// followed by a colon (that is an object key).
	jsUseRE = regexp.MustCompile(`(?:^|[^\w$.])([A-Za-z_$][\w$]*)\s*(?:[.(\[])`)
	// Any identifier at all, used to spot names that appear in a binding
	// position this file's patterns don't model (destructuring, defaults).
	jsAnyIdentRE = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
)

// webAssetKnownGlobals are names the page legitimately gets from elsewhere:
// browser built-ins beyond the shared jsGlobals list, and the framework runtime
// this markup is rendered into.
var webAssetKnownGlobals = map[string]bool{
	// core/ui runtime, injected before this script runs.
	"uiRegisterClientAction": true, "uiRegisterBlockRenderer": true, "uiOpenModal": true,
	"uiOpenSimpleModal": true, "uiOpenArtifactPane": true, "uiAlert": true, "uiConfirm": true,
	"uiRegisterMarkdownExtension": true, "fetchJSON": true, "uiToast": true, "uiPrompt": true,
	// Browser surface this file uses that the shared list doesn't name.
	"FormData": true, "FileReader": true, "Blob": true, "URL": true, "Element": true,
	"HTMLElement": true, "Node": true, "NodeList": true, "getSelection": true,
	"requestIdleCallback": true, "IntersectionObserver": true, "AbortController": true,
	"EventSource": true, "history": true, "screen": true, "scrollTo": true,
	// CSS.escape, guarded at its call sites with `window.CSS &&`.
	"CSS": true,
}

// TestWebAssetsHaveNoUndefinedIdentifiers is the guard node --check cannot be.
func TestWebAssetsHaveNoUndefinedIdentifiers(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	bodies := jsScriptBodies(string(raw))
	if len(bodies) == 0 {
		t.Fatal("no inline script found — the extractor and the asset have drifted apart")
	}

	declared := map[string]bool{}
	used := map[string]bool{}
	for _, body := range bodies {
		masked := jsMaskLiterals(body)
		collectDeclared(masked, declared)
		for _, m := range jsUseRE.FindAllStringSubmatch(masked, -1) {
			used[m[1]] = true
		}
	}

	var missing []string
	for name := range used {
		switch {
		case declared[name], jsGlobals[name], jsKeywords[name], webAssetKnownGlobals[name]:
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these names are used but never declared anywhere in the script — a reference "+
			"left behind by a removal reads as a runtime error the syntax check cannot see:\n  %s\n\n"+
			"If one of them is a legitimate global the page receives from elsewhere, add it to "+
			"webAssetKnownGlobals with a note saying where it comes from.",
			strings.Join(missing, "\n  "))
	}
}

// collectDeclared adds every binding the masked source introduces.
//
// Generous by design: a name that looks even slightly like a binding is
// recorded as one. The cost of over-collecting is a missed report; the cost of
// under-collecting is a false accusation, and this test is only useful while it
// is believed.
func collectDeclared(masked string, into map[string]bool) {
	collectVarBindings(masked, into)
	for _, re := range []*regexp.Regexp{jsDeclFuncRE, jsDeclClassRE, jsDeclCatchRE, jsDeclForRE, jsDeclMethodRE, jsDeclArrow1RE} {
		for _, m := range re.FindAllStringSubmatch(masked, -1) {
			into[m[1]] = true
		}
	}
	// Parameter lists, including destructured and defaulted ones.
	for _, m := range jsDeclParamsRE.FindAllStringSubmatch(masked, -1) {
		for _, id := range jsAnyIdentRE.FindAllString(m[1], -1) {
			into[id] = true
		}
	}
}

// collectVarBindings walks each `var`/`let`/`const` declarator list and records
// the names it BINDS, without swallowing the expressions it assigns.
//
// A regex cannot do this. `var out = [], re = /x/g, m, seen = {}` binds four
// names, and the naive pattern stops at the first `=` and finds one — which is
// how `re` and `seen` were reported as undefined on their first run. Reading
// the initializer instead is worse in the other direction: every identifier in
// `var a = foo.bar()` would count as declared, and the guard would go quiet
// about exactly the references it exists to catch.
//
// So: take the binding target of each declarator, then skip its initializer to
// the next comma at depth zero.
func collectVarBindings(masked string, into map[string]bool) {
	for _, loc := range jsDeclKeywordRE.FindAllStringIndex(masked, -1) {
		scanDeclaratorList(masked, loc[1], into)
	}
}

// scanDeclaratorList records the bindings of ONE var/let/const statement,
// starting just past the keyword. Split out because every "we are done here"
// exit has to end this statement and nothing more — returning from the caller
// instead abandoned the rest of the file, which reported almost every name in
// it as undefined.
func scanDeclaratorList(masked string, i int, into map[string]bool) {
	for i < len(masked) {
		for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t' || masked[i] == '\n' || masked[i] == '\r') {
			i++
		}
		if i >= len(masked) {
			return
		}
		// A destructuring pattern binds every name inside it.
		if masked[i] == '{' || masked[i] == '[' {
			open, closer := masked[i], byte('}')
			if open == '[' {
				closer = ']'
			}
			end := jsMatchPair(masked, i, open, closer)
			if end < 0 {
				return
			}
			for _, id := range jsAnyIdentRE.FindAllString(masked[i:end], -1) {
				into[id] = true
			}
			i = end + 1
		} else {
			start := i
			for i < len(masked) && isJSIdentByte(masked[i]) {
				i++
			}
			if i == start {
				return // not a binding position
			}
			into[masked[start:i]] = true
		}
		// Skip this declarator's initializer to the next comma at depth zero.
		depth, more := 0, false
		for i < len(masked) {
			switch c := masked[i]; {
			case c == '(' || c == '[' || c == '{':
				depth++
			case c == ')' || c == ']' || c == '}':
				if depth == 0 {
					return // the enclosing block ended
				}
				depth--
			case depth == 0 && (c == ';' || c == '\n'):
				return // statement over
			case depth == 0 && c == ',':
				more = true
			}
			i++
			if more {
				break
			}
		}
		if !more {
			return
		}
	}
}

// TestScopeGuardCatchesADanglingReference proves the guard actually fires —
// a checker that cannot fail is indistinguishable from one that passes, and
// this one exists precisely because two real breakages slipped past a green
// syntax check.
func TestScopeGuardCatchesADanglingReference(t *testing.T) {
	const removedBinding = `
      var kept = [1, 2, 3];
      function save() {
        var lost = gone.filter(function(x) { return x; }).length;
        return kept.length + lost;
      }`
	declared := map[string]bool{}
	collectDeclared(jsMaskLiterals(removedBinding), declared)
	if !declared["kept"] || !declared["save"] || !declared["lost"] || !declared["x"] {
		t.Fatalf("collector missed a real binding: %v", declared)
	}
	if declared["gone"] {
		t.Fatal("the collector invented a binding for a name nothing declares")
	}
	var flagged bool
	for _, m := range jsUseRE.FindAllStringSubmatch(jsMaskLiterals(removedBinding), -1) {
		if m[1] == "gone" {
			flagged = true
		}
	}
	if !flagged {
		t.Error("a dangling reference was not detected as a use")
	}
}

// TestScopeGuardIgnoresPropertiesAndKeys — the commonest sources of a false
// accusation. A property is not a free name, and neither is an object key.
func TestScopeGuardIgnoresPropertiesAndKeys(t *testing.T) {
	const src = `
      var o = {alpha: 1, beta: function() { return 2; }};
      o.gamma.delta();
      var s = "epsilon.zeta()";
      // eta.theta()
      o["iota"];`
	masked := jsMaskLiterals(src)
	var uses []string
	for _, m := range jsUseRE.FindAllStringSubmatch(masked, -1) {
		uses = append(uses, m[1])
	}
	for _, never := range []string{"gamma", "delta", "alpha", "epsilon", "zeta", "eta", "theta", "iota"} {
		for _, u := range uses {
			if u == never {
				t.Errorf("%q was read as a free identifier; it is a property, a key, or inside a literal", never)
			}
		}
	}
	var free []string
	for _, u := range uses {
		if !jsKeywords[u] {
			free = append(free, u)
		}
	}
	if len(free) == 0 || free[0] != "o" {
		t.Errorf("the real free identifier was missed: %v", free)
	}
}
