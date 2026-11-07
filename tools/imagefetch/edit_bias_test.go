// The decision rule has to steer a photo toward edit, not generate.
//
// The reported failure: a user sends a picture and says "make x sit in y", and
// the model draws a fresh scene the photo took no part in. Read in the wrong
// order, that request matches "wants something drawn / created" before anything
// has mentioned that a photo exists — so the ordering below is load-bearing,
// not cosmetic, and this pins it against a future tidy-up that reflows the
// description block.
package imagefetch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// OBSOLETE AS WRITTEN, and kept as the record of why.
//
// These two used to assert an ORDERING between an edit rule and a generate
// rule, and the phrasings that had to appear in the edit one — scaffolding
// built to stop the model choosing generate when a photo was involved. The
// choice is gone: there is one render action, and the only question is whether
// `images` is passed. So what is pinned now is that the decision block names
// that question, in the words people actually use.
func TestDecisionPointsAtTheImagesParameter(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc

	if strings.Contains(desc, "→ edit") {
		t.Error("edit is no longer an action to route to; the rule should point at the images parameter")
	}
	if !strings.Contains(desc, "generate WITH those pictures in images") {
		t.Errorf("the decision should say to pass the pictures, got %q", desc)
	}
	// The phrasings that actually failed in the field still have to be named —
	// that part of the old test was right and survives the merge.
	for _, want := range []string{"make x sit in y", "combine these", "this photo"} {
		if !strings.Contains(desc, want) {
			t.Errorf("decision rule should name the reported phrasing %q", want)
		}
	}
}

// With no editing backend there is nothing to pass images to, so the clause
// must not appear and promise something the deployment cannot do.
func TestNoImagesClauseWithoutAnEditor(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true,
		backends: []ImageBackendChoice{{Name: "gen"}}}).desc
	if strings.Contains(desc, "in images") {
		t.Errorf("a generate-only deployment must not mention images, got %q", desc)
	}
}

// The strongest nudge is the one that only appears when it applies.
func TestInboundMediaNoteAppearsOnlyWithAPhoto(t *testing.T) {
	set := imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}

	if d := imageSchemaFor(set).desc; strings.Contains(d, "arrived with this request") {
		t.Error("no photo this turn: the schema should not claim one arrived")
	}

	set.inboundMedia = 1
	one := imageSchemaFor(set).desc
	if !strings.Contains(one, "1 picture(s) arrived with this request") || !strings.Contains(one, "media#1") {
		t.Errorf("a single inbound photo should be named and addressable, got %q", one)
	}
	if strings.Contains(one, "media#1-media#") {
		t.Error("a single photo should not be described as a range")
	}

	set.inboundMedia = 3
	three := imageSchemaFor(set).desc
	if !strings.Contains(three, "media#1-media#3") {
		t.Errorf("three inbound photos should present as a range, got %q", three)
	}
}

// A deployment with no editor configured cannot act on the nudge, so it must
// not be told it has media to edit.
func TestNoMediaNoteWithoutAnEditor(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, inboundMedia: 2}).desc
	if strings.Contains(desc, "arrived with this request") {
		t.Error("without an edit action the media note points at nothing")
	}
}

// A connector that both generates and edits (Gemini) is offered for both: it
// makes a picture from a prompt, and edits or blends the ones it is given. It
// was filed under editing alone, so an agent with only Gemini had no way to
// make a picture from text. Named once among the backends a caller picks.
func TestABackendThatGeneratesAndEditsIsOfferedForBoth(t *testing.T) {
	gens, eds := splitBackends([]ImageBackendChoice{
		{Name: "gemini", Default: true, Edits: true, Generates: true, MaxImages: 3},
		{Name: "comfy_edit", Edits: true, MaxImages: 3},
		{Name: "comfy_gen"},
	})
	names := func(bs []ImageBackendChoice) string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Name)
		}
		return strings.Join(out, ",")
	}
	if names(gens) != "gemini,comfy_gen" || names(eds) != "gemini,comfy_edit" {
		t.Errorf("generators %s, editors %s", names(gens), names(eds))
	}
	a := imageActions{generate: true, edit: true, backends: gens, editors: eds}
	if got := strings.Join(a.backendNames(), ","); got != "comfy_edit,comfy_gen,gemini" {
		t.Errorf("selectable backends %s: a backend doing both must be listed once", got)
	}
	if a.defaultBackend() != "gemini" {
		t.Errorf("default %q", a.defaultBackend())
	}
}
