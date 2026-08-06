// A kept reference is only useful if the model knows it exists.
//
// The reported failure: the user supplies a reference image, later asks for "an
// image of x doing y", and gets a freshly invented x. The library was reachable
// only via action="help", which nothing prompted the model to call — so these
// pin that the ids reach the schema, and that a list too long to inline says so
// rather than reading as the whole library.
package imagefetch

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func editable(kept ...keptRef) imageActions {
	return imageActions{fetch: true, generate: true, edit: true, kept: kept,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}
}

func TestKeptRefsAreNamedInTheSchema(t *testing.T) {
	desc := imageSchemaFor(editable(
		keptRef{Ref: "image#wren", Label: "the user's dog"},
		keptRef{Ref: "image#brand_mark", Label: "logo to match"},
	)).desc

	for _, want := range []string{"image#wren", "the user's dog", "image#brand_mark"} {
		if !strings.Contains(desc, want) {
			t.Errorf("schema should name the kept reference %q", want)
		}
	}
	// And say what to DO with them, or naming them changes nothing.
	if !strings.Contains(desc, "pass that id in images") {
		t.Error("schema should tell the model to pass a matching reference in images")
	}
}

func TestNoKeptNoteWhenLibraryIsEmpty(t *testing.T) {
	if d := imageSchemaFor(editable()).desc; strings.Contains(d, "reference images kept") {
		t.Error("an empty library should not be announced")
	}
}

// Without an editor there is nothing to pass a reference TO.
func TestNoKeptNoteWithoutAnEditor(t *testing.T) {
	set := imageActions{fetch: true, generate: true, kept: []keptRef{{Ref: "image#wren"}}}
	if strings.Contains(imageSchemaFor(set).desc, "reference images kept") {
		t.Error("without an edit action the reference list points at nothing")
	}
}

// A truncated list that looks complete is worse than no list: the model stops
// looking for the reference it actually needs.
func TestLongLibrarySaysItIsTruncated(t *testing.T) {
	var many []keptRef
	for i := 0; i < maxSchemaKeptRefs+5; i++ {
		many = append(many, keptRef{Ref: fmt.Sprintf("image#ref%d", i)})
	}
	desc := imageSchemaFor(editable(many...)).desc

	if !strings.Contains(desc, "…and 5 more") {
		t.Errorf("a truncated library should report how many were omitted, got %q", desc)
	}
	if !strings.Contains(desc, `action="help"`) {
		t.Error("a truncated library should point at the action that lists the rest")
	}
	if strings.Contains(desc, "image#ref14") {
		t.Error("the list should stop at the cap, not print the whole library")
	}
}

// An unlabelled reference still has to be passable.
func TestUnlabelledRefStillAppears(t *testing.T) {
	if got := describeKeptRefs([]keptRef{{Ref: "image#plain"}}); got != "image#plain" {
		t.Errorf("unlabelled ref should render as its bare id, got %q", got)
	}
}

func TestKeptLabelPrefersTheAgentsOwnWords(t *testing.T) {
	// Note is why it was kept — the words a later request is likeliest to echo.
	k := KeptImage{Note: "the user's dog", Caption: "a brown terrier on a sofa"}
	if got := keptLabel(k); got != "the user's dog" {
		t.Errorf("Note should win over Caption, got %q", got)
	}
	if got := keptLabel(KeptImage{Caption: "a brown terrier"}); got != "a brown terrier" {
		t.Errorf("Caption should be used when Note is empty, got %q", got)
	}
	// A wrapped caption has to flatten: this lands inside one schema sentence.
	if got := keptLabel(KeptImage{Note: "line one\nline two"}); strings.Contains(got, "\n") {
		t.Errorf("label should collapse to a single line, got %q", got)
	}
}

func TestLabelTruncationKeepsItShort(t *testing.T) {
	long := strings.Repeat("verylongword", 10)
	if got := truncateLabel(long, maxKeptLabelChars); len(got) > maxKeptLabelChars+3 {
		t.Errorf("truncation should bound an unbroken label, got %d chars", len(got))
	}
	words := "one two three four five six seven eight nine ten eleven twelve"
	got := truncateLabel(words, maxKeptLabelChars)
	if !strings.HasSuffix(got, "…") || strings.HasSuffix(got, " …") {
		t.Errorf("a truncated label should end in an ellipsis at a word break, got %q", got)
	}
}

// A real subject is depicted from a picture of it. Generation invents a
// likeness, and for a named person that is not a worse rendering — it is
// somebody else's face.
func TestRealSubjectRuleTellsItWhereToGetAPicture(t *testing.T) {
	full := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true, find: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	if !strings.Contains(full, "never from a text prompt") {
		t.Error("the rule should refuse text-to-image for a real subject")
	}
	if !strings.Contains(full, "find one first") {
		t.Error("with search available, holding no picture means finding one")
	}

	// No search wired: the fallback is to ask, never to invent.
	noFind := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	if strings.Contains(noFind, "find one first") {
		t.Error("without search it must not tell the model to find one")
	}
	if !strings.Contains(noFind, "ask for one rather than inventing it") {
		t.Error("without search the fallback should be to ask for a picture")
	}
}

// Generated output must not be offered as a reference — the whole point.
func TestAgentMadeKeepsAreNotOfferedAsReferences(t *testing.T) {
	desc := imageSchemaFor(editable(
		keptRef{Ref: "image#wren", Label: "the user's dog"},
	)).desc
	if !strings.Contains(desc, "image#wren") {
		t.Fatal("a user-supplied reference should still be offered")
	}
	// keptRefsFor does the filtering; this pins that the schema says what a
	// reference IS, so the list cannot be read as "every picture you kept".
	if !strings.Contains(desc, "reference images kept") {
		t.Error("the list should be labelled as references")
	}
}

// The filter is the point of the whole change, so it gets a direct test rather
// than only being implied by what reaches the schema.
func TestKeptRefsForExcludesAgentMadeImages(t *testing.T) {
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}

	if RecordRecentImage(sess, []byte("A-PHOTO-OF-WREN"), "received from craig", ImageFromUser) == "" {
		t.Fatal("recording the user's photo failed")
	}
	if _, err := KeepImage(sess, "image#1", "wren", "the user's dog"); err != nil {
		t.Fatalf("keep user photo: %v", err)
	}
	if RecordRecentImage(sess, []byte("AN-INVENTED-DOG"), "generated: a dog", ImageFromGenerated) == "" {
		t.Fatal("recording the render failed")
	}
	if _, err := KeepImage(sess, "image#1", "invented_dog", "a dog I drew"); err != nil {
		t.Fatalf("keep render: %v", err)
	}

	// Both are KEPT — the change withholds reference status, it does not refuse
	// to store the picture.
	if got := len(KeptImages(sess)); got != 2 {
		t.Fatalf("both images should remain kept, got %d", got)
	}

	refs := keptRefsFor(sess)
	if len(refs) != 1 || refs[0].Ref != "image#wren" {
		t.Fatalf("only the user's photo may be offered as a reference, got %+v", refs)
	}
}

// Holding a picture of one person and not the other, the all-or-nothing reading
// generates the whole scene — and the face there WAS a photo of comes out wrong
// too. Partial references have to be stated as usable on their own.
func TestPartialReferencesAreStillUsed(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true, find: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc

	for _, want := range []string{
		"uses every reference you have and invents only the rest",
		"Never drop a reference because the set is incomplete",
		"group photo counts as a reference for each person in it",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("the mixed-subject rule should state %q", want)
		}
	}
	// Passing several references is useless if the generator can't tell which
	// is which.
	if !strings.Contains(desc, "which reference is which person") {
		t.Error("multiple references need to be identified in the prompt")
	}
}

// Without an editor there is nowhere to pass references, so the rule would be
// telling the model to do something it cannot.
func TestPartialReferenceRuleNeedsAnEditor(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, find: true}).desc
	if strings.Contains(desc, "uses every reference you have") {
		t.Error("without an edit action there is nothing to pass references to")
	}
}

// Somebody who sends a photo assumes it gets used. Offering the choice reads as
// not having understood, and asking spends a turn on a question with one
// answer.
func TestUsingAReferenceIsTheDefaultNotAnOffer(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true, find: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc

	if !strings.Contains(desc, "used by DEFAULT") {
		t.Error("the schema should state that an available reference is used by default")
	}
	if !strings.Contains(desc, "do not ask whether to use their picture") {
		t.Error("the schema should forbid asking permission to use a supplied reference")
	}
	// And it should close the loop: say which reference was used, or the user
	// cannot tell it happened.
	if !strings.Contains(desc, "say which reference you worked from") {
		t.Error("the schema should require naming the reference that was used")
	}
}

// Told to find a reference and finding nothing, the rule dead-ends: the model
// stalls, or quietly generates and hands over an invented likeness as though it
// were the person asked for.
func TestSearchFallbackIsStated(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true, find: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	if !strings.Contains(desc, "If the search turns up nothing usable, generate it and SAY you could not find a reference") {
		t.Error("the rule must say what to do when the search finds nothing")
	}
}

// A rule that only says when to search reads as "search first, always" — and a
// search for a generic subject returns somebody's photo to imitate instead of
// the picture that was asked for.
func TestGenericSubjectsAreGeneratedNotSearched(t *testing.T) {
	desc := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true, find: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	if !strings.Contains(desc, "A GENERIC subject") || !strings.Contains(desc, "do not search first") {
		t.Error("the complement should tell the model when NOT to search")
	}
	// Without a search backend there is nothing to opt out of.
	noFind := imageSchemaFor(imageActions{fetch: true, generate: true, edit: true,
		editors: []ImageBackendChoice{{Name: "comfy_lan", Default: true}}}).desc
	if strings.Contains(noFind, "do not search first") {
		t.Error("with no search wired the complement points at nothing")
	}
}
