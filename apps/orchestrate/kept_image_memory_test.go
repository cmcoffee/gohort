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
		"image#brand_mark",                          // the ref, or recall is useless
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
