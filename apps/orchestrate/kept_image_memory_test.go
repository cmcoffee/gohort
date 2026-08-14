package orchestrate

// The memory side of kept images. What matters here isn't the vector store —
// it's that core's hooks are actually wired, that the entry an agent will read
// months from now says what it needs to, and that the id is derived so a
// re-keep replaces and a forget can delete.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestKeptImageHooksAreWired(t *testing.T) {
	// core calls these on every keep/forget. Unwired, the whole feature is a
	// silent no-op — nothing fails, images just never reach memory.
	if KeptImageRemember == nil {
		t.Error("KeptImageRemember is nil — kept images would never reach memory")
	}
	if KeptImageForgetMemory == nil {
		t.Error("KeptImageForgetMemory is nil — a forgotten image would leave its memory entry behind, and recall would keep offering a dead ref")
	}
}

func TestKeptImageReportIDIsDerived(t *testing.T) {
	// Derived, not generated: re-keeping the same name must land on the same
	// id (IngestReport then replaces the stale caption), and forget must be
	// able to reach it with nothing persisted on core's side.
	a := keptImageReportID("alice", "wren", "brand_mark")
	b := keptImageReportID("alice", "wren", "brand_mark")
	if a != b {
		t.Fatalf("id is not stable across calls: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "orch-know-") {
		t.Errorf("id %q must carry the orch-know- prefix — the dedup and cap scans select on it", a)
	}
	// Different user, agent, or name must not collide: these libraries are
	// per-(user, agent) on disk and have to stay that way in memory too.
	for _, other := range []string{
		keptImageReportID("bob", "wren", "brand_mark"),
		keptImageReportID("alice", "other", "brand_mark"),
		keptImageReportID("alice", "wren", "house_style"),
	} {
		if other == a {
			t.Errorf("id collision: %q", other)
		}
	}
}

// The stored text is the whole product — it's what recall returns, and the
// agent reading it has no other context.
func TestRememberedTextTellsTheAgentHowToUseIt(t *testing.T) {
	body := keptImageMemoryBody(KeptImage{
		Name:    "brand_mark",
		Ref:     "image#brand_mark",
		Caption: "a navy circular mark with a white wordmark",
		Note:    "use on every deck",
	})
	for _, want := range []string{
		"image#brand_mark",                           // the ref, or recall is useless
		"a navy circular mark with a white wordmark", // the caption, so it can tell this from another saved image
		"use on every deck",                          // why it was kept
		"does not expire",                            // that the name stays valid
	} {
		if !strings.Contains(body, want) {
			t.Errorf("remembered text is missing %q:\n%s", want, body)
		}
	}
}

func TestRememberedTextSurvivesAMissingCaption(t *testing.T) {
	// Captioning is best-effort, so the memory entry has to read sensibly
	// without one rather than emitting a dangling "It shows:".
	body := keptImageMemoryBody(KeptImage{Name: "plain", Ref: "image#plain"})
	if strings.Contains(body, "It shows:") {
		t.Errorf("empty caption produced a dangling line:\n%s", body)
	}
	if !strings.Contains(body, "image#plain") {
		t.Errorf("ref missing:\n%s", body)
	}
}

// A description is not the picture.
//
// The reported behaviour: the agent recalls a kept image and writes a
// generation prompt from the stored description instead of passing the
// reference — so it renders something matching the WORDS rather than working
// from the picture. Recall handed it a vivid paragraph while the actual
// reference needed a tool parameter, and the earlier wording endorsed the
// substitution outright.
func TestMemoryBodyForbidsRenderingFromTheDescription(t *testing.T) {
	body := keptImageMemoryBody(KeptImage{
		Name: "wren", Ref: "image#wren",
		Description: "A brown terrier sitting on a grey sofa, late afternoon light from the left.",
		Note:        "the user's dog",
	})

	// The description still has to be there — it is how the entry is FOUND in a
	// text-only vector space.
	if !strings.Contains(body, "brown terrier") {
		t.Error("the description must stay: it is what makes the entry retrievable")
	}
	// But it must be labelled as an index entry, not as material to render from.
	if !strings.Contains(body, "NOT how you reproduce the picture") {
		t.Errorf("the body must say the description is not for rendering, got:\n%s", body)
	}
	// And say what goes wrong, since "don't" without a reason loses to
	// convenience every time.
	if !strings.Contains(body, "a different face") {
		t.Errorf("the body should name the consequence, got:\n%s", body)
	}
	// Passing the reference must read as the cheap default, not the fallback.
	if !strings.Contains(body, "passing image#wren costs nothing") {
		t.Errorf("the body should make passing the ref the obvious move, got:\n%s", body)
	}
	if !strings.Contains(body, "pass image#wren in the images list") {
		t.Errorf("the body should name exactly where the ref goes, got:\n%s", body)
	}
}

// With no description at all the entry still has to point at the picture.
func TestMemoryBodyWithoutADescriptionStillPointsAtTheRef(t *testing.T) {
	body := keptImageMemoryBody(KeptImage{Name: "mark", Ref: "image#mark", Note: "our logo"})
	if !strings.Contains(body, "pass image#mark in the images list") {
		t.Errorf("an entry with no description must still say how to use it, got:\n%s", body)
	}
}
