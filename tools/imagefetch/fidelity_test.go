// Checking that the subject survived the render.
//
// showToModel deliberately refuses to let the model judge WHO someone is —
// handed a stranger's photo, an agent that does not recognize the face has
// learned nothing, and treating that as a failure threw away correct results.
// That reasoning holds for a found picture and stops holding the moment a
// SOURCE is in hand: resemblance between two pictures is answerable from
// pixels, and it is the failure an edit model actually has.
package imagefetch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func fidelitySession(t *testing.T) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	return &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), LLM: &Session{}}
}

func TestFidelityCheckAsksTheAnswerableQuestion(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	note := fidelityCheck(sess, []string{"image#1"})
	if note == "" {
		t.Fatal("an edit with a resolvable source should carry a comparison")
	}
	// The distinction the whole thing rests on.
	if !strings.Contains(note, "not who the person is") {
		t.Errorf("must not ask the model to identify anyone, got %q", note)
	}
	if !strings.Contains(note, "SAME ONE as in the source") {
		t.Errorf("should ask about resemblance to the source, got %q", note)
	}
	// A finding has to lead somewhere, and a non-finding has to stop.
	if !strings.Contains(note, "try once more") {
		t.Error("a visible mismatch should have a next step")
	}
	if !strings.Contains(note, "do not raise this again") {
		t.Error("a survived likeness must not loop")
	}
}

// The source is put in front of the model, not just described.
func TestFidelityCheckAttachesTheSource(t *testing.T) {
	sess := fidelitySession(t)
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	before := len(sess.PendingViewImages)
	fidelityCheck(sess, []string{"image#1"})
	if len(sess.PendingViewImages) != before+1 {
		t.Error("the source should be attached for the comparison")
	}
}

// Only the first: the ordering this tool documents makes it the subject, and
// attaching all three would triple the cost of every edit.
func TestFidelityCheckShowsOnlyTheFirstSource(t *testing.T) {
	sess := fidelitySession(t)
	for i := 0; i < 3; i++ {
		if RecordRecentImage(sess, []byte{byte('A' + i)}, "received from craig", ImageFromUser) == "" {
			t.Fatal("record failed")
		}
	}
	before := len(sess.PendingViewImages)
	fidelityCheck(sess, []string{"image#1", "image#2", "image#3"})
	if got := len(sess.PendingViewImages) - before; got != 1 {
		t.Errorf("exactly one source should be attached, got %d", got)
	}
}

// No source to compare against means no question — silence beats asking
// something unanswerable.
func TestFidelityCheckIsSilentWithoutAResolvableSource(t *testing.T) {
	sess := fidelitySession(t)
	if note := fidelityCheck(sess, []string{"some-file.png"}); note != "" {
		t.Errorf("an unresolvable ref should produce no comparison, got %q", note)
	}
	if note := fidelityCheck(sess, nil); note != "" {
		t.Errorf("a render with no sources has nothing to compare, got %q", note)
	}
}

// A detached render has nobody looking this round, so attaching a source would
// spend tokens on a question no one will answer.
func TestFidelityCheckSkipsDetachedRuns(t *testing.T) {
	sess := fidelitySession(t)
	sess.Detached = true
	if RecordRecentImage(sess, []byte("SOURCE-PHOTO"), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	if note := fidelityCheck(sess, []string{"image#1"}); note != "" {
		t.Errorf("a detached run should skip the comparison, got %q", note)
	}
}
