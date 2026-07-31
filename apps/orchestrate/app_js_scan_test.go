package orchestrate

import (
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
