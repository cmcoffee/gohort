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

func TestABareMarkerCountsAsADeliveryClaim(t *testing.T) {
	// recoverStagedDeliverable used to require PROSE claiming a delivery, so a
	// reply that was only a marker — the strongest possible statement of intent,
	// since it is the framework's own send instruction — recovered nothing.
	ws := t.TempDir()
	staged := filepath.Join(ws, "gen-abc123.png")
	if err := os.WriteFile(staged, []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}

	if got := recoverStagedDeliverable(sess, "[ATTACH: gone-forever.png]"); got != "gen-abc123.png" {
		t.Errorf("a bare marker must trigger recovery, got %q", got)
	}
	// Prose claims still work, unchanged.
	if got := recoverStagedDeliverable(sess, "here's your picture"); got != "gen-abc123.png" {
		t.Errorf("a prose claim must still recover, got %q", got)
	}
	// And a reply that claims nothing still recovers nothing — the workspace is
	// not a delivery queue.
	if got := recoverStagedDeliverable(sess, "The house is on Elm Street."); got != "" {
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
