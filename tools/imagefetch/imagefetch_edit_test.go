package imagefetch

// The edit action. Generators and editors are disjoint sets — an img2img graph
// requires its input, a txt2img graph has nowhere to put one — so a backend
// belongs to exactly one action, and naming the wrong one has to say so.

import (
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func editActions(editors ...ImageBackendChoice) imageActions {
	return imageActions{
		fetch:    true,
		generate: true,
		edit:     len(editors) > 0,
		backends: []ImageBackendChoice{{Name: "comfy_txt", Default: true}},
		editors:  editors,
	}
}

// REWRITTEN for the merge. "edit" is no longer an action anyone chooses — it is
// what generate DOES when you pass images — so the thing gated on having an
// image-input backend is the images PARAMETER, not an action.
//
// The old assertion (edit present in the enum) tested the mechanism that caused
// the failure this merge removes: with two actions to pick between, "make x sit
// in y" reads as creation and generate won every time.
func TestImagesParamAppearsOnlyWithAnEditingBackend(t *testing.T) {
	none := imageSchemaFor(genActions(ImageBackendChoice{Name: "comfy_txt", Default: true}))
	if _, ok := none.params["images"]; ok {
		t.Error("the images param must not appear with no image-input backend wired")
	}
	if slices.Contains(none.params["action"].Enum, "edit") {
		t.Error("edit must never be advertised as an action")
	}
	with := imageSchemaFor(editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, NeedsPrompt: true}))
	if p, ok := with.params["images"]; !ok || p.Type != "array" {
		t.Errorf("images param = %+v, want an array", p)
	}
	if slices.Contains(with.params["action"].Enum, "edit") {
		t.Errorf("edit must not be offered even where it is supported: %v", with.params["action"].Enum)
	}
	// The single entry point is offered instead, so an edit-only backend is
	// reachable at all.
	if !slices.Contains(with.params["action"].Enum, "generate") {
		t.Errorf("generate must be offered as the one render action: %v", with.params["action"].Enum)
	}
}

func TestImagesParamNamesTheSpaceFirst(t *testing.T) {
	// image#N is what makes "edit the one you just made" work; a model that
	// doesn't know the form falls back to guessing filenames.
	s := imageSchemaFor(editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, NeedsPrompt: true}))
	d := s.params["images"].Description
	for _, want := range []string{"image#1", "media#1", "workspace filename"} {
		if !strings.Contains(d, want) {
			t.Errorf("images description missing %q:\n%s", want, d)
		}
	}
	// The refusal is easier to avoid than to explain after the fact.
	if !strings.Contains(d, "URL is NOT accepted") {
		t.Errorf("images description should rule out URLs up front:\n%s", d)
	}
}

func TestMultiImageBackendAdvertisesOrderAndCount(t *testing.T) {
	// Order decides subject vs background in a compose. Silence here produces a
	// swapped result that looks like a backend bug.
	one := imageSchemaFor(editActions(ImageBackendChoice{Name: "e1", Edits: true, MaxImages: 1, NeedsPrompt: true}))
	if strings.Contains(one.params["images"].Description, "ORDER MATTERS") {
		t.Error("a single-image backend has no order to explain")
	}
	two := imageSchemaFor(editActions(ImageBackendChoice{Name: "blend", Edits: true, MaxImages: 2}))
	d := two.params["images"].Description
	// "Up to 2" became "composes 2 pictures at a time" with count routing: the
	// number is not a CAP the model works under, it is what this deployment
	// serves and what selects the backend. A cap invited passing fewer, which a
	// compose graph cannot do — every mapped input must be filled.
	if !strings.Contains(d, "composes 2 pictures at a time") {
		t.Errorf("images description should state the servable count:\n%s", d)
	}
	if !strings.Contains(d, "ORDER MATTERS") {
		t.Errorf("a multi-image backend must explain ordering:\n%s", d)
	}
}

func TestMaskParamFollowsTheBackend(t *testing.T) {
	no := imageSchemaFor(editActions(ImageBackendChoice{Name: "e1", Edits: true, MaxImages: 1}))
	if _, ok := no.params["mask"]; ok {
		t.Error("mask must not be offered when no backend has a mask node")
	}
	yes := imageSchemaFor(editActions(ImageBackendChoice{Name: "e1", Edits: true, MaxImages: 1, AcceptsMask: true}))
	if _, ok := yes.params["mask"]; !ok {
		t.Error("mask must be offered when a backend supports inpainting")
	}
}

func TestNoTuningKnobsOnTheToolSurface(t *testing.T) {
	// Strength / denoise / blend amount live in the ComfyUI workflow, hard-set
	// by whoever built it. Exposing them invites the model to tune something it
	// has no way to evaluate.
	s := imageSchemaFor(editActions(ImageBackendChoice{Name: "e1", Edits: true, MaxImages: 2, AcceptsMask: true}))
	for _, banned := range []string{"denoise", "strength", "blend_factor", "cfg", "steps"} {
		if _, ok := s.params[banned]; ok {
			t.Errorf("param %q must not be exposed — it belongs in the workflow", banned)
		}
	}
}

func TestGeneratorBackendRefusesTheEditAction(t *testing.T) {
	avail := editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, NeedsPrompt: true})
	_, err := editImage(&ToolSession{}, map[string]any{
		"images":  []any{"image#1"},
		"prompt":  "make it snowy",
		"backend": "comfy_txt", // a generator, not an editor
	}, avail)
	if err == nil {
		t.Fatal("a text-only backend must not accept an edit")
	}
	// Worded without action names since the merge: the mismatch is between
	// what the BACKEND can do and what was asked of it, not between two
	// actions the caller might have picked.
	if !strings.Contains(err.Error(), "cannot work from a source picture") {
		t.Errorf("error should explain the mismatch: %v", err)
	}
}

func TestEditWithNoImagesSaysWhatToPass(t *testing.T) {
	avail := editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, NeedsPrompt: true})
	_, err := editImage(&ToolSession{}, map[string]any{"prompt": "make it snowy"}, avail)
	if err == nil {
		t.Fatal("edit with no source image must fail")
	}
	if !strings.Contains(err.Error(), "source image") {
		t.Errorf("error should name what's missing: %v", err)
	}
}

func TestPromptlessBackendDoesNotDemandAPrompt(t *testing.T) {
	// A blend has no text node. Requiring a prompt makes the model invent one
	// that goes nowhere.
	avail := editActions(ImageBackendChoice{Name: "blend", Edits: true, MaxImages: 2, NeedsPrompt: false})
	_, err := editImage(&ToolSession{}, map[string]any{
		"images":  []any{"image#1", "image#2"},
		"backend": "blend",
	}, avail)
	// It fails on the unreachable backend (no connector in this unit test), but
	// it must NOT fail on the missing prompt.
	if err != nil && strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("a promptless backend must not demand a prompt: %v", err)
	}

	needy := editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, NeedsPrompt: true})
	_, err = editImage(&ToolSession{}, map[string]any{
		"images":  []any{"image#1"},
		"backend": "comfy_edit",
	}, needy)
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("a prompt-driven backend must ask for one: %v", err)
	}
}

func TestImagesAcceptsASingleStringNotJustAnArray(t *testing.T) {
	// Models routinely send images="photo.png". Refusing costs a round and
	// teaches nothing.
	got := stringsArg(map[string]any{"images": "photo.png"}, "images")
	if !slices.Equal(got, []string{"photo.png"}) {
		t.Errorf("single string = %v, want it accepted as one image", got)
	}
	got = stringsArg(map[string]any{"images": []any{"a.png", "b.png"}}, "images")
	if !slices.Equal(got, []string{"a.png", "b.png"}) {
		t.Errorf("array = %v, want both preserved in order", got)
	}
	got = stringsArg(map[string]any{"images": "a.png, b.png"}, "images")
	if !slices.Equal(got, []string{"a.png", "b.png"}) {
		t.Errorf("comma string = %v, want it split", got)
	}
}

func TestEditSchemaIsDeterministic(t *testing.T) {
	set := editActions(
		ImageBackendChoice{Name: "blend", Edits: true, MaxImages: 2},
		ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 1, AcceptsMask: true, NeedsPrompt: true},
	)
	first := imageSchemaFor(set)
	for i := 0; i < 20; i++ {
		next := imageSchemaFor(set)
		if first.desc != next.desc {
			t.Fatalf("description drifted:\n%s\n%s", first.desc, next.desc)
		}
		if !slices.Equal(first.params["action"].Enum, next.params["action"].Enum) {
			t.Fatalf("action enum drifted")
		}
		if first.params["backend"].Description != next.params["backend"].Description {
			t.Fatalf("backend description drifted")
		}
	}
}

func TestGenerateNeverLandsOnAnEditor(t *testing.T) {
	// An img2img graph run with no source photo does NOT fail — it renders the
	// placeholder image baked into the workflow and hands it back as if it were
	// the answer. So generate has to refuse an editing backend outright, both
	// when the model names one and when nothing is named at all.
	avail := imageActions{
		fetch:    true,
		generate: true,
		edit:     true,
		backends: []ImageBackendChoice{{Name: "comfy_txt"}},
		editors:  []ImageBackendChoice{{Name: "qwen_edit", Edits: true, MaxImages: 1, Default: true}},
	}
	// Named explicitly — the enum lists both, so this is a legal-looking call.
	_, err := generateImage(&ToolSession{}, map[string]any{
		"prompt": "a dragon", "backend": "qwen_edit",
	}, avail)
	if err == nil {
		t.Fatal("generate on an editing backend must be refused")
	}
	if !strings.Contains(err.Error(), "can't create from text alone") {
		t.Errorf("error should explain the mismatch: %v", err)
	}

	// Omitted — and the editor is flagged as the configured default, which is
	// exactly how this reached the wrong backend in the first place.
	if got := defaultGenerateBackend(avail); got != "comfy_txt" {
		t.Errorf("default generate backend = %q, want the generator, never the editor", got)
	}
	if isGenerator(avail, "qwen_edit") {
		t.Error("an editing backend must not count as a generator")
	}
}

// REVERSED by the merge, and the concern re-homed.
//
// The worry was real: a provider pointing at an editing connector advertised
// generate with nothing behind it that could serve a text-only render. The old
// answer was to withhold the action — which, now that generate is the ONLY
// render action, would leave an edit-only deployment with no way to make a
// picture at all.
//
// So the action is offered and the LIMIT is stated instead: images required,
// text-alone impossible. The model learns it from the schema rather than from a
// failure.
func TestEditOnlyDeploymentStatesThatImagesAreRequired(t *testing.T) {
	only := imageActions{
		fetch:   true,
		edit:    true,
		editors: []ImageBackendChoice{{Name: "qwen_edit", Edits: true, MaxImages: 1, Default: true}},
	}
	only.generate = len(only.backends) > 0
	s := imageSchemaFor(only)
	if !slices.Contains(s.params["action"].Enum, "generate") {
		t.Errorf("generate is the only render action and must be offered: %v", s.params["action"].Enum)
	}
	if !strings.Contains(s.desc, "cannot create from a text prompt alone") {
		t.Errorf("an edit-only deployment must say so in the schema, got: %s", s.desc)
	}
	if !strings.Contains(s.desc, "`images` is required") {
		t.Errorf("the requirement should be explicit, got: %s", s.desc)
	}
}

func TestMissingEditCapabilityIsStated(t *testing.T) {
	// Asked to "blend the two", with no editing backend wired, a model that
	// can't see the capability at all writes a prompt describing the
	// combination and GENERATES a new picture — handing back something that
	// looks like an answer and isn't. Omitting an action hides that it exists;
	// here that silence is worse than the gap.
	noEdit := imageSchemaFor(imageActions{
		fetch:    true,
		generate: true,
		backends: []ImageBackendChoice{{Name: "comfy_txt", Default: true}},
	})
	for _, want := range []string{"nothing here can modify, blend, or combine", "not configured", "Do NOT write a prompt describing the combination"} {
		if !strings.Contains(noEdit.desc, want) {
			t.Errorf("description must state editing is unavailable (%q):\n%s", want, noEdit.desc)
		}
	}

	// With an editor present the note must vanish — it would be a lie, and it
	// tells the model not to do the thing it should be doing.
	withEdit := imageSchemaFor(editActions(ImageBackendChoice{Name: "e1", Edits: true, MaxImages: 2, NeedsPrompt: true}))
	if strings.Contains(withEdit.desc, "not configured") {
		t.Errorf("the unavailable-editing note must not appear when an editor exists:\n%s", withEdit.desc)
	}
}

func TestASavedImageIsNamedByItsFilenameNotJustItsPosition(t *testing.T) {
	// Two searches in one turn, and both results said "kept as image#1" —
	// because each one WAS image#1 when it was saved. The first had silently
	// become image#2, nothing said so, and the agent, holding two identical
	// handles for two different pictures, invented a third scheme and passed
	// ids that resolved to nothing.
	//
	// The filename is the handle that keeps meaning one picture.
	hint := editHandleHint("find-abc123.jpg", "image#1")
	if !strings.Contains(hint, "find-abc123.jpg") {
		t.Errorf("the durable handle must be the filename:\n%s", hint)
	}
	if !strings.Contains(hint, "positional") {
		t.Errorf("the hint must say image#N moves:\n%s", hint)
	}
	// It has to come BEFORE the ring ref, or the model reads to the first
	// handle offered and stops.
	if strings.Index(hint, "find-abc123.jpg") > strings.Index(hint, "image#1") {
		t.Errorf("the filename should lead, not follow the positional ref:\n%s", hint)
	}
}

func TestMediaRefIsScopedToWhatTheUserAttached(t *testing.T) {
	// An agent found two pictures in one turn and passed them as media#1 and
	// media#2, which named nothing — the description split the forms by THIS
	// TURN vs EARLIER, and finding them had happened this turn. Origin, not
	// timing, is what separates the two id spaces.
	d := imageSchemaFor(editActions(ImageBackendChoice{Name: "comfy_edit", Edits: true, MaxImages: 2, NeedsPrompt: true})).params["images"].Description
	if !strings.Contains(d, "ONLY for a photo the USER ATTACHED") {
		t.Errorf("media#N must be scoped to user-attached photos:\n%s", d)
	}
	if !strings.Contains(d, "Nothing you produced yourself is ever a media#N") {
		t.Errorf("the description must rule out produced media explicitly:\n%s", d)
	}
	// And the positional form has to admit it moves.
	if !strings.Contains(d, "SHIFT") {
		t.Errorf("image#N must say the numbers shift:\n%s", d)
	}
}
