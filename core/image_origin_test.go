// A picture the agent made is not evidence of what anything looks like.
//
// Reference images existed with no notion of provenance, so an agent could keep
// its own render and later treat it as the reference for the subject it had
// invented — each reuse compounding the invention, the depicted thing drifting
// further from the real one. These pin that origin is recorded at every
// producer, survives a keep, and cannot be laundered by re-keeping.
package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginIsRecordedAtEveryProducer(t *testing.T) {
	sess := imageSpaceSession(t)
	for _, c := range []struct {
		note   string
		origin ImageOrigin
		made   bool
	}{
		{"received from craig", ImageFromUser, false},
		{"found: a brown terrier", ImageFromFound, false},
		{"generated: a cat on a bike", ImageFromGenerated, true},
		{"edited image#2: make it night", ImageFromEdited, true},
	} {
		if RecordRecentImage(sess, testPNG(t, 8, 8), c.note, c.origin) == "" {
			t.Fatalf("record %q failed", c.note)
		}
		got := RecentImages(sess)[0]
		if got.Origin != c.origin {
			t.Errorf("%q: origin = %q, want %q", c.note, got.Origin, c.origin)
		}
		if got.Origin.AgentMade() != c.made {
			t.Errorf("%q: AgentMade = %v, want %v", c.note, got.Origin.AgentMade(), c.made)
		}
	}
}

// Unknown must not read as agent-made: the whole filter points the safe way,
// acting only on what is positively recognized.
func TestUnknownOriginIsNotAgentMade(t *testing.T) {
	if ImageOriginUnknown.AgentMade() {
		t.Error("unknown origin must not be treated as the agent's own output")
	}
}

// An entry written before origins existed still has to be classifiable, or the
// libraries that prompted this change stay unfiltered.
func TestLegacyEntriesClassifyFromTheFrameworkNote(t *testing.T) {
	cases := map[string]ImageOrigin{
		"generated: a navy circle":     ImageFromGenerated,
		"edited image#1: brighter":     ImageFromEdited,
		"received from craig":          ImageFromUser,
		"found: brown terrier":         ImageFromFound,
		"downloaded: https://x/y.png":  ImageFromFound,
		"something nobody wrote today": ImageOriginUnknown,
	}
	for note, want := range cases {
		if got := originFromNote(note); got != want {
			t.Errorf("originFromNote(%q) = %q, want %q", note, got, want)
		}
	}
}

// The sidecar has no origin field on old data; the read path must fill it in.
func TestSidecarWithoutOriginIsClassifiedOnRead(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a navy circle", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	// Rewrite the sidecar as a pre-origin one would have looked.
	dir := recentImageDir(sess)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()),
			[]byte(`{"note":"generated: a navy circle","mime":"image/png"}`), 0600); err != nil {
			t.Fatalf("rewrite meta: %v", err)
		}
	}
	if got := RecentImages(sess)[0].Origin; got != ImageFromGenerated {
		t.Errorf("legacy sidecar should classify from its note, got %q", got)
	}
}

func TestKeepCarriesOriginForward(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a navy circle", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	kept, err := KeepImage(sess, "image#1", "logo", "our mark")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if !kept.Origin.AgentMade() {
		t.Errorf("a kept render should stay marked as the agent's own, got %q", kept.Origin)
	}
	// And it must survive the round trip through the sidecar, since that is
	// what every later turn reads.
	var found bool
	for _, k := range KeptImages(sess) {
		if k.Name == "logo" {
			found = true
			if k.Origin != ImageFromGenerated {
				t.Errorf("origin lost on reload: %q", k.Origin)
			}
		}
	}
	if !found {
		t.Fatal("kept image missing after reload")
	}
}

// Re-keeping under a new name is the obvious way to launder provenance, so the
// kept entry's own origin has to be followed.
func TestRekeepingCannotLaunderAGeneratedImage(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a navy circle", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	if _, err := KeepImage(sess, "image#1", "logo", "our mark"); err != nil {
		t.Fatalf("keep: %v", err)
	}
	relaundered, err := KeepImage(sess, "image#logo", "the_real_thing", "a photo, honest")
	if err != nil {
		t.Fatalf("re-keep: %v", err)
	}
	if !relaundered.Origin.AgentMade() {
		t.Errorf("re-keeping under a new name must carry the origin forward, got %q", relaundered.Origin)
	}
}

// A user's picture must keep working as a reference — the filter is meant to
// remove invented subjects, not to empty the library.
func TestUserSuppliedImageStaysAReference(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	kept, err := KeepImage(sess, "image#1", "wren", "the user's dog")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if kept.Origin.AgentMade() {
		t.Error("an attachment from the user is not the agent's own output")
	}
}

// help and the schema have to agree about which entries are references, or the
// model gets one answer from each.
func TestManifestMarksAgentMadeEntries(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a navy circle", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	if _, err := KeepImage(sess, "image#1", "logo", "our mark"); err != nil {
		t.Fatalf("keep: %v", err)
	}
	m := KeptImageManifest(sess)
	if !strings.Contains(m, "MADE BY YOU") || !strings.Contains(m, "not a reference") {
		t.Errorf("manifest should mark the agent's own output, got %q", m)
	}
}
