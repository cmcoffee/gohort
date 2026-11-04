package workspace

// A kept image could be rendered FROM and never SENT. The asymmetry cost a real
// session: asked for three reference photos, the agent read them off its own
// manifest, failed to attach each one, invented a storage URL and 404'd, then
// offered to GENERATE fresh versions "so they become deliverable" — three
// different faces handed over as the references for three real people.

import (
	"encoding/base64"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func keptSession(t *testing.T) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	ref := RecordRecentImage(sess, testPNGBytes(), "received from craig", ImageFromUser)
	if ref == "" {
		t.Fatal("could not stage a picture")
	}
	if _, err := KeepImageOf(sess, ref, "craig_ref", "", ImageSubject{Person: true, Name: "Craig"}); err != nil {
		t.Fatal(err)
	}
	return sess
}

// A minimal real PNG — DetectContentType has to see image/png for the attach
// to pick the image channel rather than the file one.
func testPNGBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
}

func TestAKeptImageCanBeSent(t *testing.T) {
	sess := keptSession(t)
	before := len(sess.Images)

	out, err := AttachWorkspaceFile(sess, "image#craig_ref", "", false)
	if err != nil {
		t.Fatalf("a kept image must be deliverable: %v", err)
	}
	if len(sess.Images) != before+1 {
		t.Errorf("nothing reached the image channel (%d → %d)", before, len(sess.Images))
	}
	// The receiver gets a readable filename, not an id with a '#' in it.
	if !strings.Contains(out, "craig_ref.png") {
		t.Errorf("attached under a poor name: %s", out)
	}
	// And the library entry survives being sent — a reference that disappeared
	// when you shared it would be worse than one you could not share.
	if _, ok := ResolveKeptImage(sess, "image#craig_ref"); !ok {
		t.Error("sending a kept image consumed it")
	}
	if !strings.Contains(out, "still available") {
		t.Errorf("the result should say the kept copy survived: %s", out)
	}
}

func TestAnExplicitNameWins(t *testing.T) {
	sess := keptSession(t)
	out, err := AttachWorkspaceFile(sess, "image#craig_ref", "craig.png", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "craig.png") {
		t.Errorf("the caller's name should be used: %s", out)
	}
}

func TestAMissingKeptIdIsNotMistakenForAFile(t *testing.T) {
	sess := keptSession(t)
	// An id nobody kept must fail as an id. Before this, every kept id fell
	// through to the file path and reported "no such file or directory" for a
	// path that was never going to exist — which is what sent an agent looking
	// for the file, then for a URL, then for something to render.
	_, err := AttachWorkspaceFile(sess, "image#nobody_kept_this", "", false)
	if err == nil {
		t.Fatal("an unkept id must fail")
	}
	// A real workspace file still attaches the old way.
	if _, err := AttachWorkspaceFile(sess, "notes.txt", "", false); err == nil {
		t.Error("a missing ordinary file should still error")
	}
}

// A recent picture's stable id and its position are what a render's sources
// are named by, and attaching those failed: asked to show the two pictures a
// blend was made from, the agent could not send them by the ids it had, went
// looking by filename, and sent the right two files under each other's names.
// Each is now sent as itself, saying what it shows.
func TestARecentPictureIsSentByItsIdAndSaysWhatItShows(t *testing.T) {
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	ship := append(testPNGBytes(), 'E')
	other := append(testPNGBytes(), 'Y')
	_, shipID := RecordRecentImageStable(sess, ship, "found: a white starship with a saucer section", ImageFromFound)
	_, otherID := RecordRecentImageStable(sess, other, "found: a red and gray battleship", ImageFromFound)
	if shipID == "" || otherID == "" {
		t.Fatal("no stable ids to attach by")
	}
	for _, c := range []struct {
		id, shows string
		data      []byte
	}{{shipID, "white starship", ship}, {otherID, "red and gray battleship", other}} {
		before := len(sess.Images)
		out, err := AttachWorkspaceFile(sess, c.id, "", false)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if len(sess.Images) != before+1 || sess.Images[before] != base64.StdEncoding.EncodeToString(c.data) {
			t.Errorf("%s did not send its own picture", c.id)
		}
		if !strings.Contains(out, "It shows: ") || !strings.Contains(out, c.shows) {
			t.Errorf("%s: the result does not say what it shows: %s", c.id, out)
		}
	}
	// By position too: image#1 is the newest. A picture of its own, since a
	// reply carries each picture once and the two above are already in it.
	third := append(testPNGBytes(), 'T')
	RecordRecentImage(sess, third, "found: a third picture", ImageFromFound)
	before := len(sess.Images)
	if _, err := AttachWorkspaceFile(sess, "image#1", "", false); err != nil || len(sess.Images) != before+1 || sess.Images[before] != base64.StdEncoding.EncodeToString(third) {
		t.Errorf("image#1 did not send the newest picture: %v", err)
	}
}
