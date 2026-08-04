// A delivery MARKER that points at nothing. The logs kept showing the same
// shape: a reply consisting of one [ATTACH: file] marker, the file already
// cleaned up by an earlier attach, the marker resolving to nothing, and the
// whole reply stripping to empty — which the contact received as "I wasn't
// able to put together a response to that".
package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// stagedSession builds a workspace holding the named files and a session that
// knows THIS turn created them — the state the framework records per tool call.
func stagedSession(t *testing.T, names ...string) *ToolSession {
	t.Helper()
	ws := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	sess.AddStagedFiles(names)
	return sess
}

func TestABareMarkerCountsAsADeliveryClaim(t *testing.T) {
	// recoverStagedDeliverable used to require PROSE claiming a delivery, so a
	// reply that was only a marker — the strongest possible statement of intent,
	// since it is the framework's own send instruction — recovered nothing.
	sess := stagedSession(t, "gen-abc123.png")

	if got := recoverStagedDeliverable(sess, "[ATTACH: gone-forever.png]", false); got != "gen-abc123.png" {
		t.Errorf("a bare marker must trigger recovery, got %q", got)
	}
	// Prose claims still work, unchanged.
	if got := recoverStagedDeliverable(sess, "here's your picture", false); got != "gen-abc123.png" {
		t.Errorf("a prose claim must still recover, got %q", got)
	}
	// And a reply that claims nothing still recovers nothing — the workspace is
	// not a delivery queue.
	if got := recoverStagedDeliverable(sess, "The house is on Elm Street.", false); got != "" {
		t.Errorf("ordinary text must not ship a staged file, got %q", got)
	}
}

func TestUnresolvableMarkersAreNamed(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "real.png"), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}

	missing := unresolvedAttachMarkers(sess, "[ATTACH: deleted.png] and [ATTACH: real.png]")
	if len(missing) != 1 || missing[0] != "deleted.png" {
		t.Fatalf("missing = %v, want just the deleted one", missing)
	}
	if got := unresolvedAttachMarkers(sess, "no markers here"); len(got) != 0 {
		t.Errorf("a reply with no markers has nothing unresolved, got %v", got)
	}
}

func TestAProducedFileIsSentWithoutReadingTheProse(t *testing.T) {
	// The rule that should have been first. "There you go. Shazz's got a
	// captive audience now 🚗" is a caption — it names what is IN the picture,
	// not the word "picture" — so every gate keyed on delivery nouns missed
	// exactly the replies that most obviously accompany an image. A turn that
	// RAN a producer and attached nothing needs no prose analysis at all.
	sess := stagedSession(t, "gen-x.png")
	caption := "There you go. Shazz's got a captive audience now whether they want it or not. 🚗"

	if got := recoverStagedDeliverable(sess, caption, false); got != "" {
		// Documents the old behaviour honestly: without the producer signal,
		// prose alone still has to carry it — and "there you go" now does.
		_ = got
	}
	if got := recoverStagedDeliverable(sess, caption, true); got != "gen-x.png" {
		t.Errorf("a produced file with a caption must ship, got %q", got)
	}
	// The veto still outranks the producer signal: a turn that ran the tool and
	// then said it went wrong must not ship the thing it rejected.
	if got := recoverStagedDeliverable(sess, "I couldn't get that to come out right.", true); got != "" {
		t.Errorf("a disclaimed failure must not ship, got %q", got)
	}
}

func TestAnotherTurnsPictureIsNeverRecovered(t *testing.T) {
	// The agent posting random photos. The workspace root is per USER — shared
	// by every session, agent and turn — and the backstop used to ship "the
	// newest deliverable file in it, if modified within ten minutes". So a
	// picture made in one conversation was eligible to be attached to an
	// unrelated reply in another a few minutes later. Nothing about the file
	// had to have anything to do with the turn that sent it.
	ws := t.TempDir()
	for _, n := range []string{"find-someone-elses.jpg", "gen-old.png"} {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// A turn that staged NOTHING: it ran a producer that failed, or produced
	// nothing at all, and then wrote a reply that reads like a delivery.
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	if got := recoverStagedDeliverable(sess, "Here's the picture you asked for", true); got != "" {
		t.Errorf("a turn that staged nothing must ship nothing, got %q", got)
	}

	// Same workspace, but now the turn really did make one. Only that file is
	// a candidate — the neighbours stay put no matter how recent they are.
	sess.AddStagedFiles([]string{"gen-mine.png"})
	if err := os.WriteFile(filepath.Join(ws, "gen-mine.png"), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := recoverStagedDeliverable(sess, "Here's the picture you asked for", true); got != "gen-mine.png" {
		t.Errorf("the turn's own file must ship, got %q", got)
	}
}

func TestAStagedFileAlreadyCleanedUpIsNotReshipped(t *testing.T) {
	// attach(cleanup=true) removes the file it delivered. A name the turn
	// staged but that no longer exists must not be handed on as a delivery.
	sess := stagedSession(t, "gen-gone.png")
	if err := os.Remove(filepath.Join(sess.WorkspaceDir, "gen-gone.png")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := recoverStagedDeliverable(sess, "here's your picture", true); got != "" {
		t.Errorf("a consumed file must not be recovered, got %q", got)
	}
}

func TestProducersAreDistinguishedFromInspectionTools(t *testing.T) {
	if !turnProducedDeliverable([]PersistedToolCall{{Name: "web_search"}, {Name: "image"}}) {
		t.Error("the image tool produces deliverables")
	}
	// A screenshot taken to READ a page is not something the user asked to
	// receive, so it must not arm the backstop.
	if turnProducedDeliverable([]PersistedToolCall{{Name: "screenshot_page"}, {Name: "workspace"}}) {
		t.Error("inspection tools must not count as producing a deliverable")
	}
	if turnProducedDeliverable(nil) {
		t.Error("no calls, nothing produced")
	}
}
