package imagefetch

// Collapsing the per-connector generate_image_<name> tools into one `image`
// tool turns the backend from a tool NAME into a parameter. Two things have to
// hold: the advertised enum lists only backends this caller can reach, and the
// handler re-checks — a filtered enum is a hint to the model, not a boundary.

import (
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
