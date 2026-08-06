package orchestrate

// What the two inbound paths TEACH about media#N ids has to match, because the
// ids themselves already do. The chat path said "pass these to the image tool
// to edit, blend, or combine"; the channel path described them only as
// forwarding handles and warned against calling a tool for them at all. Asked
// over iMessage to "make x sit in y", a model did the only image thing it had
// been told about: it generated a new picture instead of editing the one it
// was sent.

import (
	"strings"
	"testing"
)

func TestChannelManifestTeachesEditing(t *testing.T) {
	m := buildInboundMediaManifest("Alice", 1)
	if m == "" {
		t.Fatal("no manifest for a turn carrying an image")
	}
	for _, want := range []string{
		"media#1",         // the handle
		"action=\"edit\"", // and what to do with it
		"images=[\"media#1\"]",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest never mentions %q:\n%s", want, m)
		}
	}
	// The specific failure: generating instead of editing.
	if !strings.Contains(m, "Do NOT generate a new picture") {
		t.Error("manifest does not warn against generating when asked to change a photo")
	}
}

// The anti-echo rule has to survive, and has to stop applying to edits — an
// edited picture is a new one and IS the answer to what was asked.
func TestChannelManifestStillBlocksEchoButAllowsEdits(t *testing.T) {
	m := buildInboundMediaManifest("Alice", 2)
	if !strings.Contains(m, "do NOT send one back UNCHANGED") {
		t.Error("the echo guard is gone; a photo just posted would be re-sent to the same group")
	}
	if !strings.Contains(m, "An EDITED version is a new picture and is fine to send here") {
		t.Error("nothing says an edit may be sent back, so the echo guard reads as blocking the answer itself")
	}
	if !strings.Contains(m, "media#2") {
		t.Error("both items must be individuated")
	}
}

func TestNoManifestWithoutMedia(t *testing.T) {
	if m := buildInboundMediaManifest("Alice", 0); m != "" {
		t.Errorf("manifest emitted for a turn with no media: %q", m)
	}
}
