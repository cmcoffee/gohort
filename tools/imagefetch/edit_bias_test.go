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

// editRuleComesFirst: whichever rule the model reads first is the one that
// claims an ambiguous request.
func TestEditRuleComesBeforeGenerateRule(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	edit, gen := strings.Index(desc, "→ edit"), strings.Index(desc, "→ generate")
	if edit < 0 || gen < 0 {
		t.Fatalf("both decision rules should be present, got %q", desc)
	}
	if edit > gen {
		t.Errorf("the edit rule must be read before the generate rule; edit at %d, generate at %d", edit, gen)
	}
}

// The phrasings that actually failed in the field belong in the rule, so the
// match is on the user's words rather than on the model's inference from them.
func TestEditRuleNamesTheReportedPhrasings(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	for _, want := range []string{"make x sit in y", "put x in it", "combine these"} {
		if !strings.Contains(desc, want) {
			t.Errorf("decision rule should name the reported phrasing %q", want)
		}
	}
	// And generate has to give the case up explicitly, or it keeps winning on
	// the strength of "make".
	if !strings.Contains(desc, "FROM NOTHING") {
		t.Error("the generate rule should exclude requests about an existing picture")
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
