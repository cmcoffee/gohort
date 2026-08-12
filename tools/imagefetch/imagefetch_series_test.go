package imagefetch

// "I'll do you a few variations" used to end after one. The first render
// detached, the model was told not to call the tool again, and nothing carried
// the promise past the end of the turn. These cover the half that carries it:
// the count, and the instruction that starts the next piece.

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestADeclaredSetLeavesAnInstructionToStartTheNext(t *testing.T) {
	const chat = "sess-imgseries-1"
	t.Cleanup(func() { CloseTaskSeries(chat, "image") })
	tool := &ImageTool{}
	sess := &ToolSession{ChatSessionID: chat, Detached: true}

	out := tool.noteSeriesPiece(sess, map[string]any{"prompt": "a red bicycle", "variations": 3}, "Stored.")
	if !strings.Contains(out, "picture 1 of the 3") {
		t.Errorf("the result must say where in the set this is:\n%s", out)
	}
	cont := sess.TakeTaskContinuation()
	if !strings.Contains(cont, "PIECE 1 OF 3") {
		t.Fatalf("the delivering turn must be told to start the next one:\n%s", cont)
	}
	if !strings.Contains(cont, "a red bicycle") {
		t.Errorf("the continuation must name the idea — the wake turn has nothing else to go on:\n%s", cont)
	}

	// Second piece: the model does not re-declare the count, and must not have
	// to. Only the first call carries it.
	sess2 := &ToolSession{ChatSessionID: chat, Detached: true}
	out = tool.noteSeriesPiece(sess2, map[string]any{"prompt": "a red bicycle, from above"}, "Stored.")
	if !strings.Contains(out, "picture 2 of the 3") {
		t.Errorf("the count must survive a call that omits it:\n%s", out)
	}
	if !strings.Contains(sess2.TakeTaskContinuation(), "PIECE 2 OF 3") {
		t.Error("the second piece must still ask for the third")
	}

	// Last piece: nothing more to start, and the set is closed behind it.
	sess3 := &ToolSession{ChatSessionID: chat, Detached: true}
	out = tool.noteSeriesPiece(sess3, map[string]any{"prompt": "a red bicycle at night"}, "Stored.")
	if !strings.Contains(out, "picture 3 of the 3") {
		t.Errorf("the final piece must still say which it is:\n%s", out)
	}
	if c := sess3.TakeTaskContinuation(); c != "" {
		t.Errorf("the set is finished — nothing more should be started:\n%s", c)
	}
	if TaskSeriesOpen(chat, "image") {
		t.Error("the set must close itself when the last piece lands")
	}
}

func TestASinglePictureStartsNothing(t *testing.T) {
	tool := &ImageTool{}
	sess := &ToolSession{ChatSessionID: "sess-imgseries-2", Detached: true}
	out := tool.noteSeriesPiece(sess, map[string]any{"prompt": "a red bicycle"}, "Stored.")
	if out != "Stored." {
		t.Errorf("an ordinary render must be left alone:\n%s", out)
	}
	if c := sess.TakeTaskContinuation(); c != "" {
		t.Errorf("nothing was declared, so nothing should follow:\n%s", c)
	}
}

// Inline, the turn is still here — but the nudge to keep going has to COUNT and
// has to STOP. The version that did neither said "call image again now" after
// every render, identically, forever: eighteen pictures, five delivered.
func TestAnInlineSetCountsAndThenStops(t *testing.T) {
	tool := &ImageTool{}
	sess := &ToolSession{ChatSessionID: "sess-imgseries-3"}
	args := map[string]any{"prompt": "a red bicycle", "variations": 3}

	for i := 1; i <= 2; i++ {
		sess.NextImageAttempt(false) // the render the tool would have counted
		out := tool.noteSeriesPiece(sess, args, "Stored.")
		if !strings.Contains(out, fmt.Sprintf("picture %d of the 3", i)) {
			t.Errorf("render %d must say where in the set it is:\n%s", i, out)
		}
		// The attach must come first. Naming the next render as the next thing
		// to do is what left seventeen pictures sitting in the workspace.
		if !strings.Contains(out, "Attach THIS one now") {
			t.Errorf("render %d must be delivered before the next is started:\n%s", i, out)
		}
	}

	sess.NextImageAttempt(false)
	out := tool.noteSeriesPiece(sess, args, "Stored.")
	if !strings.Contains(out, "LAST of the 3") || !strings.Contains(out, "Do NOT render another") {
		t.Errorf("the set must terminate at the number the model asked for:\n%s", out)
	}
	if c := sess.TakeTaskContinuation(); c != "" {
		t.Errorf("an inline call has its own next round — no wake instruction:\n%s", c)
	}
	if TaskSeriesOpen("sess-imgseries-3", "image") {
		t.Error("an inline set must not open a ledger entry nothing will close")
	}
}

// The ceiling that was lost when the grouped tool replaced the one that had it.
func TestRendersAreCappedPerTurn(t *testing.T) {
	sess := &ToolSession{ChatSessionID: "sess-imgcap"}
	for i := 0; i < ImageGenHardCap(); i++ {
		if err := checkRenderBudget(sess); err != nil {
			t.Fatalf("render %d refused early: %v", i+1, err)
		}
	}
	err := checkRenderBudget(sess)
	if err == nil {
		t.Fatal("a turn must not render past the ceiling — eighteen in seven minutes is what that costs")
	}
	// Refused with an instruction to deliver, not a bare limit: a model told
	// only "no" goes looking for another route.
	for _, want := range []string{"attach every picture you have not delivered", "Do NOT call image again this turn"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say what to do instead (%q): %v", want, err)
		}
	}
}

// A detached render is rationed by the detach ledger instead; counting it here
// would refuse the later pieces of a legitimate set.
func TestTheCapLeavesDetachedRendersAlone(t *testing.T) {
	sess := &ToolSession{ChatSessionID: "sess-imgcap-2", Detached: true}
	for i := 0; i < ImageGenHardCap()+5; i++ {
		if err := checkRenderBudget(sess); err != nil {
			t.Fatalf("detached render %d refused: %v", i+1, err)
		}
	}
}

func TestALonePictureGetsNoSetNote(t *testing.T) {
	sess := &ToolSession{ChatSessionID: "sess-imgseries-3b"}
	sess.NextImageAttempt(false)
	out := (&ImageTool{}).noteSeriesPiece(sess, map[string]any{"prompt": "a red bicycle"}, "Stored.")
	if out != "Stored." {
		t.Errorf("one picture is not a set:\n%s", out)
	}
}

// A set that started detached and finishes inline — a faster backend, a raised
// threshold — has nothing left to carry. Leaving the count open would renumber
// whatever renders next.
func TestASetThatGoesInlineStopsCountingRatherThanRenumbering(t *testing.T) {
	const chat = "sess-imgseries-4"
	t.Cleanup(func() { CloseTaskSeries(chat, "image") })
	tool := &ImageTool{}
	tool.noteSeriesPiece(&ToolSession{ChatSessionID: chat, Detached: true},
		map[string]any{"prompt": "a red bicycle", "variations": 3}, "Stored.")

	tool.noteSeriesPiece(&ToolSession{ChatSessionID: chat},
		map[string]any{"prompt": "a red bicycle, from above"}, "Stored.")
	if TaskSeriesOpen(chat, "image") {
		t.Error("a set finishing inline must close, or a later render becomes piece 3 of nothing")
	}
}

// The schema is the only place the model learns the count exists. Without it
// the promise is made in prose and kept by nobody.
func TestTheSchemaOffersTheCountWhereRendersAreOffered(t *testing.T) {
	s := imageSchemaFor(imageActions{generate: true})
	p, ok := s.params["variations"]
	if !ok {
		t.Fatal("a deployment that can render must be able to declare a set")
	}
	if p.Type != "integer" {
		t.Errorf("variations type = %q, want integer", p.Type)
	}
	if !strings.Contains(p.Description, "FIRST call") {
		t.Errorf("the description must say the count is declared once:\n%s", p.Description)
	}
	// And it must not appear where nothing can render — a find/fetch-only
	// deployment offering a variations count is advertising work it cannot do.
	if _, ok := imageSchemaFor(imageActions{find: true, fetch: true}).params["variations"]; ok {
		t.Error("no render backend means no sets")
	}
}

// The misfire that cost a whole request: image(prompt=…) with no action came
// back as the usage spec, the agent read it as success, announced four
// variations, and delivered two pictures from an earlier session. The
// arguments said exactly what was meant.
func TestARenderWithNoActionIsInferredNotAnswerredWithTheManual(t *testing.T) {
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"a prompt is a render", map[string]any{"prompt": "an armada of generational ships"}, "generate"},
		{"sources are a render", map[string]any{"images": []string{"image#1"}}, "generate"},
		{"a declared set is a render", map[string]any{"prompt": "x", "variations": 3}, "generate"},
		{"a query is a search", map[string]any{"query": "funny cat"}, "find"},
		{"a url is a download", map[string]any{"url": "https://example.com/a.png"}, "fetch"},
	} {
		if got, why := inferImageAction(c.args); got != c.want {
			t.Errorf("%s: inferred %q (%s), want %q", c.name, got, why, c.want)
		}
	}
}

func TestAmbiguousAndBareCallsAreNotGuessed(t *testing.T) {
	// Two readings. Picking one silently is how the wrong thing gets delivered
	// with nothing to show a guess was made.
	if got, why := inferImageAction(map[string]any{"prompt": "x", "url": "https://e/a.png"}); got != "" || why == "" {
		t.Errorf("ambiguous call inferred %q (%s), want no guess and a reason", got, why)
	}
	// keep/label/forget share their params — three verbs, no inference.
	if got, why := inferImageAction(map[string]any{"name": "brand_mark"}); got != "" || why == "" {
		t.Errorf("keep-shaped call inferred %q (%s), want no guess and a reason", got, why)
	}
	// A genuinely bare call is a probe, and the manual is the right answer.
	if got, why := inferImageAction(map[string]any{}); got != "" || why != "" {
		t.Errorf("bare call = %q/%q, want the manual", got, why)
	}
	// An empty string is not an argument. A model that fills every field sends
	// prompt:"" on a fetch, and that must not decide the action.
	if got, _ := inferImageAction(map[string]any{"prompt": "  ", "url": "https://e/a.png"}); got != "fetch" {
		t.Errorf("blank prompt swung the inference: got %q, want fetch", got)
	}
}

// Whatever else the spec says, it must not read like a result.
func TestTheUsageSpecSaysItRenderedNothing(t *testing.T) {
	out, err := (&ImageTool{}).RunWithSession(map[string]any{"action": "help"}, &ToolSession{})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.HasPrefix(out, "NOTHING WAS RENDERED") {
		t.Errorf("the spec must open by saying it is not a result:\n%s", out)
	}
}

// A render is background work because of what it is, not how long it takes:
// twenty seconds is fast and still holds the conversation shut for twenty
// seconds, and a set of four holds it for eighty.
func TestRendersGoToTheBackgroundWhateverTheyCost(t *testing.T) {
	tool := &ImageTool{}
	if !tool.AlwaysDetach(map[string]any{"action": "generate", "prompt": "a red bicycle"}, nil) {
		t.Error("a generate must not hold the turn open")
	}
	if !tool.AlwaysDetach(map[string]any{"action": "edit", "images": []string{"image#1"}}, nil) {
		t.Error("an edit must not hold the turn open")
	}
	// A download has nothing to wait for, and a wake per fetch is noise.
	for _, a := range []string{"fetch", "find", "keep", "help"} {
		if tool.AlwaysDetach(map[string]any{"action": a}, nil) {
			t.Errorf("%s must stay inline", a)
		}
	}
}

// The inference has to be visible to every pre-call check. Read the literal
// argument only and a call whose action was inferred is judged as having none:
// no estimate, so no detach, and preflight skipped on the way past.
func TestAnInferredRenderIsStillTreatedAsARender(t *testing.T) {
	tool := &ImageTool{}
	args := map[string]any{"prompt": "a red bicycle"} // no action, as the model wrote it
	if effectiveImageAction(args) != "generate" {
		t.Fatalf("effective action = %q, want generate", effectiveImageAction(args))
	}
	if !tool.AlwaysDetach(args, nil) {
		t.Error("an inferred render must detach like a named one")
	}
	// An explicit action always wins over the arguments.
	if got := effectiveImageAction(map[string]any{"action": "help", "prompt": "x"}); got != "help" {
		t.Errorf("named action = %q, want it to win", got)
	}
}
