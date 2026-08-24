package orchestrate

// The preview's whole value is that it is the REAL composed block. A
// re-implementation would drift, and a drifting preview is worse than
// none: it teaches something false about what the model sees.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func previewFixture() MachineDef {
	return MachineDef{
		Name: "Investigation", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Desc: "Is there something to explain?", Prompt: "Work out which kind of turn this is.",
				Next: "answer", NextFrom: "next_phase",
				Output: []PipelineField{
					{Name: "next_phase", Type: "string", Desc: "where to go"},
					{Name: "observation", Type: "string", Desc: "what was seen"},
				}},
			{Name: "hunch", Desc: "Form one hypothesis.", Prompt: "Commit to one explanation.", Next: "verify",
				Output: []PipelineField{{Name: "hypothesis", Type: "string", Desc: "the best explanation"}}},
			{Name: "verify", Desc: "Test it.", Prompt: "Go and look.", Resident: true},
		},
	}
}

func TestPreviewIsTheRealComposedBlock(t *testing.T) {
	def := previewFixture()
	verify, _ := def.Phase("verify")
	raw, _ := json.Marshal(phasePreview(def, verify))
	html := string(raw)

	// It must contain what PhaseBlock actually produces — the same
	// function a live turn calls — not a paraphrase of it.
	want := def.PhaseBlock(verify, sampleStateFor(def, verify), PhaseVars{})
	for _, line := range []string{"Established earlier in this conversation", "Other phases in this workflow", "Go and look."} {
		if !strings.Contains(want, line) {
			t.Fatalf("fixture does not exercise %q — the test would prove nothing", line)
		}
		if !strings.Contains(html, HTMLEscape(line)) {
			t.Errorf("the preview omits %q, which the model WILL see", line)
		}
	}

	// Earlier steps' findings arrive LABELLED — which is the thing an
	// author otherwise hand-copies into the prompt. The block humanises
	// the field name ("observation" → "Observation:") and drops the label
	// entirely when a step declares just one field, so assert what the
	// model actually sees rather than the raw key.
	for _, f := range []string{"Observation:", "what was seen", "the best explanation"} {
		if !strings.Contains(html, HTMLEscape(f)) {
			t.Errorf("the preview should show that %q already arrives", f)
		}
	}
	// A step's own fields are NOT in its established block — it cannot
	// read what it is about to write.
	if strings.Contains(want, "‹where to go›") && strings.Contains(want, "### triage") {
		tri, _ := def.Phase("triage")
		own := def.PhaseBlock(tri, sampleStateFor(def, tri), PhaseVars{})
		if strings.Contains(own, "### triage") {
			t.Error("a step was shown its own findings as established")
		}
	}
	// And it says plainly that repeating any of it is unnecessary.
	if !strings.Contains(html, "not need to repeat") {
		t.Error("the preview should say the author does not have to restate this")
	}
}

// Placeholders, not invented values: there is no conversation yet, and a
// preview showing made-up findings would read as data.
func TestPreviewUsesPlaceholdersNotFakeFindings(t *testing.T) {
	def := previewFixture()
	verify, _ := def.Phase("verify")
	st := sampleStateFor(def, verify)
	res, ok := st["triage"]
	if !ok {
		t.Fatal("earlier steps should appear in the sample state")
	}
	v, _ := res.Fields["observation"].(string)
	if !strings.HasPrefix(v, "‹") {
		t.Errorf("expected a placeholder, got %q — a preview showing invented findings reads as data", v)
	}
	if !strings.Contains(v, "what was seen") {
		t.Errorf("the placeholder should carry the field's own description: %q", v)
	}
	// A step declaring nothing still shows that it contributes a reply.
	def.Phases = append(def.Phases, MachinePhase{Name: "chatty", Prompt: "talk"})
	st2 := sampleStateFor(def, verify)
	if !strings.Contains(st2["chatty"].Text, "replied") {
		t.Errorf("a step with no declared fields should still show what it contributes: %+v", st2["chatty"])
	}
}

// The two kinds of step are composed differently, and the preview's only
// value is being the real thing. Its first version called PhaseBlock for
// both — showing a transient step an "Established earlier" block it never
// receives, and hiding the output contract it does.
func TestPreviewMatchesHowEachKindIsActuallyComposed(t *testing.T) {
	def := previewFixture()

	tri, _ := def.Phase("triage")
	trans := def.PhaseInstructions(tri, sampleStateFor(def, tri), PhaseVars{MachineTurn: MachineTurn{Input: "‹msg›"}})
	// A transient step gets the CONTRACT — its declared fields, each with
	// the description the author wrote. That is the interlink between the
	// fields and the instruction, and it was invisible.
	if !strings.Contains(trans, `"observation"`) || !strings.Contains(trans, "what was seen") {
		t.Errorf("a transient step should receive its fields as instructions:\n%s", trans)
	}
	// And what earlier steps established, which it did NOT used to get —
	// the asymmetry that had authors hand-copying {state:…} references
	// for values the definition already knows.
	if !strings.Contains(trans, "Established earlier in this conversation") {
		t.Errorf("a transient step should be handed what earlier steps worked out:\n%s", trans)
	}
	// Unless it reaches for them itself, in which case a second copy
	// would be the framework arguing with the author.
	tri.Prompt = "Explain {state:hunch.hypothesis} in one line."
	own := def.PhaseInstructions(tri, sampleStateFor(def, tri), PhaseVars{MachineTurn: MachineTurn{Input: "‹msg›"}})
	if strings.Contains(own, "Established earlier in this conversation") {
		t.Errorf("a prompt that places its own references should not also get the block:\n%s", own)
	}

	// A resident step gets the block and NOT a contract: its reply goes to
	// the person, not to a decoder.
	ver, _ := def.Phase("verify")
	res := def.PhaseInstructions(ver, sampleStateFor(def, ver), PhaseVars{MachineTurn: MachineTurn{Input: "‹msg›"}})
	if !strings.Contains(res, "Established earlier in this conversation") {
		t.Errorf("a resident step should receive what earlier steps established:\n%s", res)
	}
	if strings.Contains(res, "Reply with a single JSON object") {
		t.Error("a resident step must not be handed an output contract")
	}
}

// The preview is the surface that teaches what the framework composes,
// so it must not show the pre-edit composition after a save. It
// refreshes in place (never a page reload — the prompt saves on a
// typing debounce, and reloading would yank the page out from under
// somebody mid-sentence), driven by the framework's own invalidation
// broadcast.
func TestThePreviewRefreshesAfterASave(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "look",
		Phases: []MachinePhase{
			{Name: "look", Prompt: "Original instruction.", Next: "reply"},
			{Name: "reply", Prompt: "Answer.", Resident: true},
		}})

	// The endpoint the refresh calls, and the page render, come from one
	// function — so what a save re-fetches cannot drift from what was
	// drawn.
	r := httptest.NewRequest("GET", "/x?name=look&preview=1", nil)
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("preview fetch failed: %d %s", w.Code, w.Body.String())
	}
	var got struct{ Block, Note string }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Block, "Original instruction.") {
		t.Errorf("the block should be the composed step: %q", got.Block)
	}
	if got.Note == "" {
		t.Error("the framing sentence should come with it — it changes with the step's shape")
	}
	block, note := phasePreviewParts(def, def.Phases[0])
	if block != got.Block || note != got.Note {
		t.Error("the endpoint and the page render must be the same function")
	}

	// The rendered card carries the step name the broadcast will match.
	raw, _ := json.Marshal(phasePreview(def, def.Phases[0]))
	if !strings.Contains(string(raw), `data-preview-step=\"look\"`) {
		t.Errorf("the preview must be findable by step name: %s", raw)
	}
	for _, want := range []string{"data-preview-body", "data-preview-note"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the refresh writes into %s, which the card does not have", want)
		}
	}
	// And the listener keys off the same endpoint a phase form writes to.
	page, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "'/phases?name='") {
		t.Error("the refresh should key off the save endpoint the framework broadcasts")
	}
	if !strings.Contains(string(page), "JS(machinePreviewRefreshJS)") {
		t.Error("the listener is written but never installed")
	}
}

// The panel is titled "What this step actually receives" and its note said
// "this is what the step is actually told" for every kind of step. For two
// kinds that is false: a tool step runs with no model in the loop at all (the
// composed text only fills the tool's arguments), and a step that pins values
// and asks nothing returns before any call is made. An author tuning wording on
// either one is tuning something no model reads, and finds out by running the
// machine.
func TestPreviewNoteNamesWhoReceivesTheText(t *testing.T) {
	def := MachineDef{Name: "m"}

	toolStep := MachinePhase{Name: "fetch", Tool: "http_get", Prompt: "get the page"}
	_, note := phasePreviewParts(def, toolStep)
	if !strings.Contains(note, "http_get") || !strings.Contains(note, "NO model") {
		t.Errorf("a tool step's note must say no model is asked and name the tool; got: %s", note)
	}

	// A step that asks for nothing and says nothing, but pins a field, returns
	// before any model call — mirror of the early return in MachineDef.runPhase.
	pinOnly := MachinePhase{Name: "pin", Output: []PipelineField{{Name: "id", From: "input"}}}
	if len(pinOnly.StaticFields()) == 0 {
		t.Skip("fixture does not produce a static-only step; the branch is pinned by the runner instead")
	}
	_, note = phasePreviewParts(def, pinOnly)
	if !strings.Contains(note, "NO model") {
		t.Errorf("a pin-only step's note must say no model runs; got: %s", note)
	}

	// An ordinary model step keeps the original framing.
	plain := MachinePhase{Name: "think", Prompt: "consider the options"}
	_, note = phasePreviewParts(def, plain)
	if strings.Contains(note, "NO model") {
		t.Errorf("an ordinary step still goes to a model; got: %s", note)
	}
}
