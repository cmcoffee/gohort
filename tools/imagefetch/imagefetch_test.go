package imagefetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	. "github.com/cmcoffee/gohort/core"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Collapsing the per-connector generate_image_<name> tools into one `image`
// tool turns the backend from a tool NAME into a parameter. Two things have to
// hold: the advertised enum lists only backends this caller can reach, and the
// handler re-checks — a filtered enum is a hint to the model, not a boundary.

func genActions(backends ...ImageBackendChoice) imageActions {
	return imageActions{find: true, fetch: true, generate: true, backends: backends}
}

func TestBackendParamOnlyWhenThereIsAChoice(t *testing.T) {
	// One backend is not a choice — advertising a single-value enum is pure
	// schema weight and invites the model to "pick" something it can't change.
	one := imageSchemaFor(genActions(ImageBackendChoice{Name: "comfy_lan", Default: true}))
	if _, ok := one.params["backend"]; ok {
		t.Error("backend param must be omitted when only one backend is reachable")
	}
	none := imageSchemaFor(genActions())
	if _, ok := none.params["backend"]; ok {
		t.Error("backend param must be omitted when no backend is selectable")
	}
	two := imageSchemaFor(genActions(
		ImageBackendChoice{Name: "comfy_lan", Default: true},
		ImageBackendChoice{Name: "dalle"},
	))
	p, ok := two.params["backend"]
	if !ok {
		t.Fatal("backend param must appear when more than one backend is reachable")
	}
	if !slices.Equal(p.Enum, []string{"comfy_lan", "dalle"}) {
		t.Errorf("backend enum = %v, want both in sorted order", p.Enum)
	}
}

func TestBackendParamNamesTheDefault(t *testing.T) {
	// Omitting `backend` has to be a predictable choice, not a mystery.
	s := imageSchemaFor(genActions(
		ImageBackendChoice{Name: "comfy_lan", Default: true},
		ImageBackendChoice{Name: "dalle"},
	))
	desc := s.params["backend"].Description
	if !strings.Contains(desc, "default (comfy_lan)") {
		t.Errorf("backend description must name the default:\n%s", desc)
	}
}

func TestPromptGuidanceSurvivesTheCollapse(t *testing.T) {
	// Each backend's prompting quirks used to live on its own tool description.
	// JSON Schema has no per-enum-value docs, so they fold into the one param
	// description — without this the guidance is simply lost.
	s := imageSchemaFor(genActions(
		ImageBackendChoice{Name: "comfy_lan", Default: true, Guidance: "quote any words you want rendered."},
		ImageBackendChoice{Name: "dalle", Guidance: "keep prompts under 60 words."},
	))
	desc := s.params["backend"].Description
	// Assert the guidance TEXT survives and stays attached to its own backend —
	// not the exact sentence shape. The description also carries each backend's
	// role now, and pinning the punctuation between the two just breaks on
	// wording changes that lose nothing.
	for _, want := range []string{"quote any words you want rendered", "keep prompts under 60 words"} {
		if !strings.Contains(desc, want) {
			t.Errorf("backend description dropped guidance %q:\n%s", want, desc)
		}
	}
	if strings.Index(desc, "quote any words") < strings.Index(desc, "comfy_lan") {
		t.Errorf("guidance must follow the backend it belongs to:\n%s", desc)
	}
	if strings.Index(desc, "keep prompts under") < strings.Index(desc, "dalle") {
		t.Errorf("guidance must follow the backend it belongs to:\n%s", desc)
	}
}

func TestBackendEnumOrderIsStable(t *testing.T) {
	// The enum sits at the front of the prompt; reordering it between turns
	// re-pays cold prefill.
	set := genActions(
		ImageBackendChoice{Name: "alpha"},
		ImageBackendChoice{Name: "beta", Default: true},
		ImageBackendChoice{Name: "gamma"},
	)
	first := imageSchemaFor(set).params["backend"]
	for i := 0; i < 20; i++ {
		next := imageSchemaFor(set).params["backend"]
		if !slices.Equal(first.Enum, next.Enum) {
			t.Fatalf("backend enum drifted: %v vs %v", first.Enum, next.Enum)
		}
		if first.Description != next.Description {
			t.Fatalf("backend description drifted:\n%s\n%s", first.Description, next.Description)
		}
	}
}

func TestGenerateAvailableWhenOnlyAConnectorBackendExists(t *testing.T) {
	// A rest_image connector is a complete image provider on its own — the
	// generate action must not depend on a built-in Gemini/OpenAI key.
	a := imageActions{fetch: true, backends: []ImageBackendChoice{{Name: "comfy_lan"}}}
	a.generate = len(a.backends) > 0
	s := imageSchemaFor(a)
	if !slices.Contains(s.params["action"].Enum, "generate") {
		t.Errorf("generate missing from enum %v with a connector backend present", s.params["action"].Enum)
	}
}

func TestUnreachableBackendIsRefusedAtRun(t *testing.T) {
	// ENFORCEMENT, not UX. A model can name a backend that was never in its
	// enum — stale context, a copied call, a hallucinated name. Nothing may
	// dispatch on it.
	tool := &ImageTool{}
	sess := &ToolSession{}
	if ImageBackendReachable(sess, "definitely_not_a_backend") {
		t.Fatal("an unregistered backend must not be reachable")
	}
	_, err := tool.RunWithSession(map[string]any{
		"action":  "generate",
		"prompt":  "a cat",
		"backend": "definitely_not_a_backend",
	}, sess)
	if err == nil {
		t.Fatal("generate with an unreachable backend must fail")
	}
	// The refusal must not read as a transient failure, or the model retries.
	if !strings.Contains(err.Error(), "not available to you") &&
		!strings.Contains(err.Error(), "no image-generation provider is configured") {
		t.Errorf("error = %q, want an explicit availability refusal", err)
	}
}

func TestOmittedBackendIsAlwaysAllowed(t *testing.T) {
	// Empty means "the configured default" — the pre-collapse behavior of every
	// caller. It must never be gated, or existing agents break.
	if !ImageBackendReachable(&ToolSession{}, "") {
		t.Error("an omitted backend must resolve to the default, not be refused")
	}
	if !ImageBackendReachable(&ToolSession{}, "default") {
		t.Error(`the literal "default" must resolve to the default`)
	}
	if !ImageBackendReachable(nil, "") {
		t.Error("a nil session must not refuse the default backend")
	}
}

// A render that outlives its turn. Ordinarily the tool stages the picture and
// tells the model to attach it — there is a next round, and the model is in it.
// A detached render has no next round: the turn that asked ended while it was
// still rendering. Whatever it produces has to be attached HERE or it is
// stored, announced as finished, and never sent to anyone.

// stagedPNG writes a real (tiny) PNG somewhere the tool will read it from, the
// way a finished backend render arrives on disk.
func stagedPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	path := filepath.Join(t.TempDir(), "render.png")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestDetachedRenderAttachesItself(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), Detached: true}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "edit", "edited: a cat", ImageFromEdited)
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if len(sess.Images) != 1 {
		t.Fatalf("a detached render must attach its own output — nobody downstream will (%d attached)", len(sess.Images))
	}
	// And the text must not send the model after a path it should not touch:
	// attaching again is how the same picture went out twice.
	if strings.Contains(msg, "workspace(action=\"attach\"") && !strings.Contains(msg, "Do NOT call workspace") {
		t.Errorf("detached result should not ask for an attach it already did:\n%s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "attached") {
		t.Errorf("the model needs to know the picture is already attached:\n%s", msg)
	}
}

func TestInlineRenderStillLeavesDeliveryToTheModel(t *testing.T) {
	// The inline path is unchanged on purpose. A generate can be an intermediate
	// step — the picture that gets blended, not the one that gets sent — so on a
	// turn that HAS a next round the model still decides what goes out.
	ws := t.TempDir()
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat", ImageFromGenerated)
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if len(sess.Images) != 0 {
		t.Errorf("inline renders must not auto-attach: %d attached", len(sess.Images))
	}
	if !strings.Contains(msg, "workspace(action=\"attach\"") {
		t.Errorf("inline result should still ask for the attach:\n%s", msg)
	}
	// Either way the file is staged in the workspace.
	entries, _ := os.ReadDir(ws)
	if len(entries) == 0 {
		t.Error("the render should be saved to the workspace")
	}
}

// The edit action. Generators and editors are disjoint sets — an img2img graph
// requires its input, a txt2img graph has nowhere to put one — so a backend
// belongs to exactly one action, and naming the wrong one has to say so.

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

// What a search does when nothing scored a confident match. Three endings, not
// two — and the one that was missing is the one a request for a picture of a
// specific PERSON lands on.

func TestAnAbstainingScreenIsNotADownloadFailure(t *testing.T) {
	// Asking a vision model to confirm a named person's identity is the
	// question it most often declines: it answers in prose with no trailing
	// rating, every candidate comes back unscored, and the search reported
	// "could not download any usable image" — sending you to debug a network
	// problem that never happened, while six good photographs sat in hand.
	if got := findOutcome(6, 0, -1); got != findScreenAbstained {
		t.Errorf("six images fetched, none rated → %v, want findScreenAbstained", got)
	}
	// Nothing downloaded at all is the real download failure.
	if got := findOutcome(0, 0, -1); got != findNoImages {
		t.Errorf("no usable images → %v, want findNoImages", got)
	}
	// Rated and genuinely wrong stays a rejection — that message is accurate
	// and worth keeping.
	if got := findOutcome(4, 4, 20); got != findAllRejected {
		t.Errorf("four rated, best 20/100 → %v, want findAllRejected", got)
	}
	// A partial screen still counts as screened: something answered.
	if got := findOutcome(5, 1, 10); got != findAllRejected {
		t.Errorf("one of five rated → %v, want findAllRejected", got)
	}
}

func TestAnUnratedReplyScoresAsNoAnswer(t *testing.T) {
	// The mechanism behind the above: a decline carries no number, and "no
	// answer" must not read as "rated zero".
	if got := parseTrailingScore("I can't identify people in photographs."); got != -1 {
		t.Errorf("a refusal scored %d, want -1 (no answer)", got)
	}
	if got := parseTrailingScore("A person standing outdoors.\n0"); got != 0 {
		t.Errorf("an explicit zero scored %d, want 0", got)
	}
	if got := parseTrailingScore("Looks right.\n95"); got != 95 {
		t.Errorf("scored %d, want 95", got)
	}
}

// The grouped `image` tool advertises only the actions whose backing config
// exists. Offering `find` with no serper key produced a call that could only
// fail — and the model, having no way to predict that, retried it.
//
// The rules are tested through imageActions/imageSchemaFor rather than through
// live config, so a machine with (or without) a search provider gets the same
// answer.

func schemaFor(a imageActions) imageSchema { return imageSchemaFor(a) }

func TestActionsNarrowToWhatIsConfigured(t *testing.T) {
	cases := []struct {
		name string
		set  imageActions
		want []string
	}{
		// `help`, `keep` and `forget` ride along on every non-empty set: all
		// three are framework-side (the image space), so no backend config can
		// take them away. help is how the model finds out which pictures it can
		// still reference; keep/forget are how it decides which ones outlive
		// the ring.
		{"all configured", imageActions{find: true, fetch: true, generate: true}, []string{"find", "fetch", "generate", "help", "keep", "label", "forget"}},
		{"no search provider", imageActions{fetch: true, generate: true}, []string{"fetch", "generate", "help", "keep", "label", "forget"}},
		{"no image gen", imageActions{find: true, fetch: true}, []string{"find", "fetch", "help", "keep", "label", "forget"}},
		{"fetch only", imageActions{fetch: true}, []string{"fetch", "help", "keep", "label", "forget"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := schemaFor(c.set)
			enum := s.params["action"].Enum
			if !slices.Equal(enum, c.want) {
				t.Errorf("action enum = %v, want %v", enum, c.want)
			}
			// A param for an action that can't run is dead weight in the schema.
			if _, ok := s.params["query"]; ok != c.set.find {
				t.Errorf("query param present = %v, want %v", ok, c.set.find)
			}
			if _, ok := s.params["prompt"]; ok != c.set.generate {
				t.Errorf("prompt param present = %v, want %v", ok, c.set.generate)
			}
			if _, ok := s.params["url"]; !ok {
				t.Error("url param must always be present — fetch needs no config")
			}
		})
	}
}

func TestNoActionsMeansUnavailable(t *testing.T) {
	// Never ship an empty enum: it invalidates the whole tool payload for the
	// turn. Nil params tells the catalog to drop the tool instead.
	s := schemaFor(imageActions{})
	if s.params != nil {
		t.Errorf("params = %v, want nil so the tool is dropped", s.params)
	}
	if s.desc != "" {
		t.Errorf("desc = %q, want empty", s.desc)
	}
}

func TestDescriptionNeverNamesAnUnavailableAction(t *testing.T) {
	// The prose and the enum have to agree — a description mentioning `generate`
	// while the enum omits it is worse than either alone.
	s := schemaFor(imageActions{find: true, fetch: true})
	for _, banned := range []string{"generate (", "→ generate"} {
		if containsStr(s.desc, banned) {
			t.Errorf("description names the unavailable generate action (%q):\n%s", banned, s.desc)
		}
	}
	if !containsStr(s.desc, "find (") || !containsStr(s.desc, "fetch (") {
		t.Errorf("description omits an available action:\n%s", s.desc)
	}
}

func TestSchemaIsDeterministic(t *testing.T) {
	// Tool schemas sit at the front of the prompt; drift between turns re-pays
	// cold prefill. The map iteration inside imageSchemaFor must not leak out.
	set := imageActions{find: true, fetch: true, generate: true}
	first, err := json.Marshal(schemaFor(set).params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	firstDesc := schemaFor(set).desc
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(schemaFor(set).params)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(first) != string(next) {
			t.Fatalf("params drifted:\n first: %s\n  next: %s", first, next)
		}
		if d := schemaFor(set).desc; d != firstDesc {
			t.Fatalf("description drifted:\n first: %s\n  next: %s", firstDesc, d)
		}
	}
}

func TestStaticSchemaKeepsFullShape(t *testing.T) {
	// Desc()/Params() feed the global semantic tool index and the session-less
	// pickers, which must see every action regardless of local config.
	tool := &ImageTool{}
	enum := tool.Params()["action"].Enum
	if !slices.Equal(enum, []string{"find", "fetch", "generate", "help", "keep", "label", "forget"}) {
		t.Errorf("static action enum = %v, want all three backend actions plus the three framework-side ones", enum)
	}
	if tool.Desc() == "" {
		t.Error("static description must not be empty — the tool index embeds it")
	}
}

func TestUnavailableActionExplainsItself(t *testing.T) {
	// A model can name an action that wasn't in its enum (stale context, a
	// copied call). The refusal has to say WHY and whether retrying helps.
	tool := &ImageTool{}
	if !isDynamic(tool) {
		t.Fatal("ImageTool must implement core.DynamicChatTool")
	}
	// generate is gated on live config; when it's off the error must name the
	// cause rather than fall through to "unknown action".
	if !ImageGenerationAvailable() {
		_, err := tool.RunWithSession(map[string]any{"action": "generate", "prompt": "x"}, nil)
		if err == nil {
			t.Fatal("generate with no provider must fail")
		}
		if !containsStr(err.Error(), "no image-generation provider is configured") {
			t.Errorf("error = %q, want the configuration reason", err)
		}
	}
	_, err := tool.RunWithSession(map[string]any{"action": "bogus"}, nil)
	if err == nil || !containsStr(err.Error(), "unknown action") {
		t.Errorf("error = %v, want an unknown-action error", err)
	}
}

func isDynamic(v any) bool {
	_, ok := v.(DynamicChatTool)
	return ok
}

func containsStr(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestEveryActionTheDescriptionAdvertisesIsCallable(t *testing.T) {
	// The invariant that broke. The description had said "help." since it was
	// written, the enum never listed it, and on a grammar-constrained backend an
	// enum is not advice — the sampler cannot emit a value that isn't in it. So
	// the documented way to ask "which pictures can I still reference?" was
	// unreachable, and a turn that needed it went guessing at filenames instead.
	//
	// Checked as a rule rather than for `help` alone: prose and enum drifting
	// apart is the failure, and it can drift on any action.
	for _, set := range []imageActions{
		{find: true, fetch: true, generate: true, edit: true},
		{fetch: true},
		{generate: true, edit: true},
	} {
		s := schemaFor(set)
		enum := s.params["action"].Enum
		for _, name := range []string{"find", "fetch", "generate", "edit", "help"} {
			// "name (" is how each action introduces itself in the prose.
			if containsStr(s.desc, name+" (") && !slices.Contains(enum, name) {
				t.Errorf("description advertises %q but the enum omits it (enum=%v)", name, enum)
			}
		}
	}
}

func TestHelpNeverResurrectsAnUnavailableTool(t *testing.T) {
	// help needs no backend, so appending it to the enum must not turn a
	// deployment with nothing wired into a tool that can only describe itself.
	if s := schemaFor(imageActions{}); s.params != nil {
		t.Errorf("params = %v, want nil — help alone is not a working image tool", s.params)
	}
}

// A vision screen that cannot see is not a screen that says no.
//
// Asked to rate how well a picture depicts "house", a model with no image
// modality answers that it cannot see any image and then, obediently, rates it
// 0. That reads as a genuine rejection, so every candidate "fails" and the
// search reports "none clearly depict it (best visual match 0/100) — refine the
// query" while holding perfectly good photographs of houses. The caller then
// rewords a query that was never the problem.

func TestABlindScreenIsAnAbstentionNotAZero(t *testing.T) {
	blind := []string{
		"I don't see an image in your message. Could you attach it?\n0",
		"There is no image provided, so I cannot describe it. 0",
		"I'm unable to see images. As a text-based model I can only read text.\n0",
		"I cannot process images, but based on the description: 0",
	}
	for _, answer := range blind {
		if !ModelSawNoImage(answer) {
			t.Errorf("not recognized as a blind screen, so its 0 will be read as a rejection: %q", answer)
		}
	}

	// A screen that actually looked must never be downgraded, at either end of
	// the range — a real 0 is a real rejection and has to keep rejecting.
	sighted := []string{
		"The image shows a two-story suburban house with a green lawn.\n95",
		"This is a photograph of a cat sitting on a windowsill, not a house.\n0",
		"A close-up of a bicycle wheel against gravel. 12",
		"The image shows an empty room with no furniture.\n30",
	}
	for _, answer := range sighted {
		if ModelSawNoImage(answer) {
			t.Errorf("a screen that described the picture was treated as blind: %q", answer)
		}
	}
}

// The sentinel has to sort below both a real rating and a missing one, or the
// loop's existing arithmetic quietly promotes it: `score >= 0` would count it
// as rated, and `score > bestScore` would let it become the best candidate.
func TestBlindSentinelCountsAsNeitherRatedNorBest(t *testing.T) {
	if scoreScreenBlind >= 0 {
		t.Error("a blind screen would be counted as having rated the candidate")
	}
	if bestScore := -1; scoreScreenBlind > bestScore {
		t.Error("a blind screen would become the best match, beating an honest abstention")
	}
	if scoreScreenBlind >= imageMatchThreshold {
		t.Error("a blind screen would pass the acceptance threshold")
	}
}

// "I'll do you a few variations" used to end after one. The first render
// detached, the model was told not to call the tool again, and nothing carried
// the promise past the end of the turn. These cover the half that carries it:
// the count, and the instruction that starts the next piece.

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
