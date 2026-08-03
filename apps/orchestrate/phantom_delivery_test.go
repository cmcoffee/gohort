// A reply that promises a file it never made. Observed as exactly
// "[ATTACH: find-dkfindcraig.jpg]" — a filename with the right shape and the
// subject's name stuffed into it, for a picture the turn never produced (it
// called no tools at all).
package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAPromisedFileThatWasNeverMadeIsAPhantom(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()} // empty workspace
	refs := phantomDeliveryRefs(sess, "[ATTACH: find-dkfindcraig.jpg]")
	if len(refs) != 1 || refs[0] != "find-dkfindcraig.jpg" {
		t.Fatalf("refs = %v, want the invented filename", refs)
	}
}

func TestADeliveredFileIsNotAPhantomEvenAfterCleanup(t *testing.T) {
	// The 21:07 case: the picture WAS delivered, then cleanup=true removed it,
	// and the reply still carries a marker naming it. That is a duplicate
	// reference, not a lie — correcting the model here would spend a round
	// telling it that it failed when it succeeded.
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	sess.AppendImage("ALREADY-DELIVERED")
	if refs := phantomDeliveryRefs(sess, "[ATTACH: gone.png]"); len(refs) != 0 {
		t.Errorf("a turn that delivered something has no phantom, got %v", refs)
	}
}

func TestARecoverableFileIsNotAPhantom(t *testing.T) {
	// The backstop will ship the staged file, so there is nothing to correct.
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "gen-real.png"), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	if refs := phantomDeliveryRefs(sess, "[ATTACH: mistyped-name.png]"); len(refs) != 0 {
		t.Errorf("a recoverable delivery has no phantom, got %v", refs)
	}
}

func TestTheFallbackSaysWhatActuallyWentWrong(t *testing.T) {
	// "Could you rephrase it" blames the request, and the request was fine.
	text, deliver := channelDelivery("", nil, nil, false, true)
	if !deliver || text != channelPhantomFallback {
		t.Fatalf("a phantom delivery must say so, got %q", text)
	}
	if got, _ := channelDelivery("", nil, nil, false, false); got != channelEmptyFallback {
		t.Errorf("an ordinary empty reply keeps the generic line, got %q", got)
	}
	// Attachments and real text still outrank both.
	if got, _ := channelDelivery("here you go", nil, nil, false, true); got != "here you go" {
		t.Errorf("real text must pass through, got %q", got)
	}
	if _, deliver := channelDelivery("", nil, nil, true, true); deliver {
		t.Error("a deliberate silence still delivers nothing")
	}
}
