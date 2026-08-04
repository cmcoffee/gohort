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
	refs := phantomDeliveryRefs(sess, "[ATTACH: find-dkfindcraig.jpg]", "")
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
	if refs := phantomDeliveryRefs(sess, "[ATTACH: gone.png]", "image"); len(refs) != 0 {
		t.Errorf("a turn that delivered something has no phantom, got %v", refs)
	}
}

func TestARecoverableFileIsNotAPhantom(t *testing.T) {
	// The backstop will ship the staged file, so there is nothing to correct.
	sess := stagedSession(t, "gen-real.png")
	if refs := phantomDeliveryRefs(sess, "[ATTACH: mistyped-name.png]", ""); len(refs) != 0 {
		t.Errorf("a recoverable delivery has no phantom, got %v", refs)
	}
}

func TestAMarkerIsAPhantomWhenTheTurnMadeNothing(t *testing.T) {
	// Same reply, same workspace contents — but the files belong to some other
	// turn. There is nothing to recover, so the model has to be told, rather
	// than the backstop quietly handing over a picture from elsewhere.
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "gen-someone-elses.png"), []byte("\x89PNG\r\n\x1a\nfake"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	refs := phantomDeliveryRefs(sess, "[ATTACH: mistyped-name.png]", "")
	if len(refs) != 1 || refs[0] != "mistyped-name.png" {
		t.Errorf("an unrecoverable claim must be corrected, got %v", refs)
	}
}

func TestACaptionForAPictureThatWasNeverMadeIsAPhantom(t *testing.T) {
	// The reported failure: asked to generate an image, the turn ran the image
	// tool, produced nothing, and replied "Here's you, wasting away in the
	// garage like Craig ordered." No marker, so the marker rule had nothing to
	// look at. No delivery noun either — a caption names what is IN the picture,
	// never the picture — so the prose rule had nothing to look at. The tool
	// call is the only evidence there is, and it is enough.
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	refs := phantomDeliveryRefs(sess, "Here's you, wasting away in the garage like Craig ordered.", "image")
	if len(refs) != 1 || refs[0] != "the image" {
		t.Fatalf("refs = %v, want the kind of thing the turn was making", refs)
	}
	// A video producer names a video.
	if refs := phantomDeliveryRefs(sess, "Here you go!", "video"); len(refs) != 1 || refs[0] != "the video" {
		t.Errorf("refs = %v, want the video", refs)
	}
}

func TestACaptionIsOnlyAPhantomWhenTheTurnWasMakingSomething(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	// No producer ran: prose alone must never convict. "Here's" opens half the
	// replies in the product, and a correction round for each one is worse than
	// the bug — this rule is deliberately blind without a tool call behind it.
	if refs := phantomDeliveryRefs(sess, "Here's what I think about that.", ""); len(refs) != 0 {
		t.Errorf("ordinary talk is not a phantom, got %v", refs)
	}
	// Producer ran, but the reply isn't presenting anything — it's asking a
	// question or reporting. Nothing was claimed, so nothing is a lie.
	for _, reply := range []string{
		"I found three candidates. Which one did you have in mind?",
		"That prompt keeps tripping the content filter.",
	} {
		if refs := phantomDeliveryRefs(sess, reply, "image"); len(refs) != 0 {
			t.Errorf("a reply that claims nothing is not a phantom: %q → %v", reply, refs)
		}
	}
	// Producer ran and FAILED, and the reply says so. The disclaimer is the
	// model getting it right; correcting it would be the framework getting it
	// wrong.
	if refs := phantomDeliveryRefs(sess, "I wasn't able to generate that image — here's what went wrong.", "image"); len(refs) != 0 {
		t.Errorf("an honest failure is not a phantom, got %v", refs)
	}
}

func TestACaptionWithARealDeliveryIsNotAPhantom(t *testing.T) {
	// The success path has to stay quiet, or every generated image costs a
	// correction round.
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	sess.AppendImage("REAL-BYTES")
	if refs := phantomDeliveryRefs(sess, "Here's you, wasting away in the garage.", "image"); len(refs) != 0 {
		t.Errorf("a delivered image is not a phantom, got %v", refs)
	}
	// And a caption over a file the backstop can still ship is not one either —
	// recoverClaimedDelivery attaches it, so there is nothing to correct.
	staged := stagedSession(t, "gen-real.png")
	if refs := phantomDeliveryRefs(staged, "Here you go — one garage.", "image"); len(refs) != 0 {
		t.Errorf("a recoverable delivery is not a phantom, got %v", refs)
	}
}

func TestAPresentedDeliveryNeedsNoDeliveryNoun(t *testing.T) {
	// The split that makes the caption case reachable: replyClaimsAttachment
	// still demands a noun (it gates a backstop that SHIPS a file on prose
	// alone), replyPresentsDelivery does not.
	caption := "Here's you, wasting away in the garage like Craig ordered."
	if replyClaimsAttachment(caption) {
		t.Error("the noun rule must stay strict — it gates shipping a file")
	}
	if !replyPresentsDelivery(caption) {
		t.Error("a caption is still written as a hand-over")
	}
	if replyPresentsDelivery("I couldn't make that one.") {
		t.Error("a disclaimer outranks every cue")
	}
}

func TestTheProducerKindNamesWhatWasMissed(t *testing.T) {
	if got := turnDeliverableKind([]PersistedToolCall{{Name: "web_search"}, {Name: "generate_image"}}); got != "image" {
		t.Errorf("kind = %q, want image", got)
	}
	if got := turnDeliverableKind([]PersistedToolCall{{Name: "download_video"}}); got != "video" {
		t.Errorf("kind = %q, want video", got)
	}
	if got := turnDeliverableKind([]PersistedToolCall{{Name: "screenshot_page"}}); got != "" {
		t.Errorf("an inspection tool makes no deliverable, got %q", got)
	}
}

func TestTheDeliveryWatchRemembersTheFirstProducer(t *testing.T) {
	// The loop hands rounds in one at a time; the guard asks once, at the end.
	var w deliveryWatch
	if w.producedKind() != "" {
		t.Error("nothing run yet")
	}
	w.note([]ToolCall{{Name: "recall"}})
	if w.producedKind() != "" {
		t.Error("recall makes no deliverable")
	}
	w.note([]ToolCall{{Name: "generate_image"}})
	w.note([]ToolCall{{Name: "download_video"}})
	if got := w.producedKind(); got != "image" {
		t.Errorf("kind = %q — what the turn set out to make, not the last thing it touched", got)
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
