package orchestrate

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestMissingArgsNamesOnlyTheEmptyOne is the whole point of the helper. The
// error it replaced listed every required argument on any failure, and a live
// agent that had sent url and compare_op correctly read that as a verdict on
// all three: six rounds of re-sending the same call and dropping arguments
// that were never the problem. An argument the caller DID supply must never
// appear in the complaint.
func TestMissingArgsNamesOnlyTheEmptyOne(t *testing.T) {
	err := missingArgs("http_poll monitor \"status\"",
		reqArg{"url", "https://example.invalid/status", "the address fetched"},
		reqArg{"compare_op", "==", "one of < > <= >= == != contains"},
		reqArg{"threshold", "", "the value compared against"},
	)
	if err == nil {
		t.Fatal("an empty threshold must still be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "threshold") {
		t.Errorf("the missing argument is not named: %s", msg)
	}
	for _, supplied := range []string{"url", "compare_op"} {
		if strings.Contains(msg, supplied+" ") || strings.Contains(msg, " "+supplied) {
			t.Errorf("%q was supplied but the error still complains about it: %s", supplied, msg)
		}
	}
	// The hint belongs to the missing argument only, so the caller is told what
	// to put there rather than left to guess the format.
	if !strings.Contains(msg, "the value compared against") {
		t.Errorf("the missing argument's hint is not shown: %s", msg)
	}
	if strings.Contains(msg, "the address fetched") {
		t.Errorf("a supplied argument's hint is being shown: %s", msg)
	}
	// And it says the rest of the call survived, because the caller cannot see
	// that: without it the natural repair is to rebuild the call from scratch.
	if !strings.Contains(msg, "everything else you sent is fine") {
		t.Errorf("the error does not say the rest of the call was accepted: %s", msg)
	}
}

// TestMissingArgsWritesAListLikeASentence: several missing arguments read as
// prose, and whitespace-only counts as missing (an LLM filling a required
// field with " " has not supplied it).
func TestMissingArgsWritesAListLikeASentence(t *testing.T) {
	err := missingArgs("poll monitor \"x\"",
		reqArg{"check_agent", "  ", ""},
		reqArg{"check", "", ""},
	)
	if err == nil {
		t.Fatal("whitespace is not a supplied value")
	}
	if !strings.Contains(err.Error(), "check_agent and check") {
		t.Errorf("two missing arguments should read as a list: %s", err)
	}
	if !strings.Contains(err.Error(), "them") {
		t.Errorf("the plural repair instruction is wrong: %s", err)
	}
}

// TestMissingArgsIsSilentWhenNothingIsMissing keeps the call site a one-liner.
func TestMissingArgsIsSilentWhenNothingIsMissing(t *testing.T) {
	if err := missingArgs("watch monitor \"x\"", reqArg{"tool_name", "read_chat", "hint"}); err != nil {
		t.Errorf("a complete call must not error: %v", err)
	}
}

// A delivery MARKER that points at nothing. The logs kept showing the same
// shape: a reply consisting of one [ATTACH: file] marker, the file already
// cleaned up by an earlier attach, the marker resolving to nothing, and the
// whole reply stripping to empty — which the contact received as "I wasn't
// able to put together a response to that".

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

// A turn that started background work has not made its picture yet: what it
// staged is what the job builds from. Observed 2026-10-24: "First pass is
// running" after four photo searches went out with the fourth photo, a source
// of the blend still rendering.
func TestAReplyWhileAPassRunsShipsNoSourcePhoto(t *testing.T) {
	sess := stagedSession(t, "find-temple.jpg")
	sess.ClaimDetachSlot("image")
	if got := recoverStagedDeliverable(sess, "Here's the plan: first pass is running", true); got != "" {
		t.Errorf("a source of the running pass went out as the delivery: %q", got)
	}
	// The model's own send instruction still counts.
	if got := recoverStagedDeliverable(sess, "[ATTACH: gone.jpg]", true); got != "find-temple.jpg" {
		t.Errorf("a marker should still recover, got %q", got)
	}
}

// Recognizing a delivery the model THINKS it made. This gates the backstop that
// ships a file the reply promised and nothing attached — the "here's your
// image" with no image that kept coming back.

func TestADeliveryClaimIsRecognizedHoweverItIsPhrased(t *testing.T) {
	for _, claim := range []string{
		"Here's your picture of a house!",
		"Here is the image you asked for.",
		"here you go — one haunted mansion, photo attached",
		"I've attached the screenshot.",
		"Sending you the pdf now.",
		// The phrasings that actually come back and used to sit outside the
		// cue list entirely — a model announcing what it MADE is claiming
		// delivery as much as one saying "here you go".
		"Done — your haunted house image is ready!",
		"I made you a picture of a cat 🐱",
		"I've generated the image.",
		"All set! One spooky mansion picture for you.",
	} {
		if !replyClaimsAttachment(claim) {
			t.Errorf("not recognized as a delivery claim: %q", claim)
		}
	}
}

func TestAFailureIsNotADeliveryClaim(t *testing.T) {
	// The dangerous false positive: a reply that says it went WRONG still
	// mentions a picture, and shipping a staged file on the back of it hands
	// the user the very thing the model just rejected.
	for _, notClaim := range []string{
		"I couldn't find a picture that matches.",
		"I wasn't able to generate the image.",
		"I found an image but it doesn't match what you described.",
		"No luck on the photo — the sources blocked the download.",
		"Failed to create the picture.",
	} {
		if replyClaimsAttachment(notClaim) {
			t.Errorf("a failure was read as a delivery claim: %q", notClaim)
		}
	}
}

func TestOrdinaryTalkIsNotADeliveryClaim(t *testing.T) {
	for _, plain := range []string{
		"Here's what I think about that.",
		"I made a reservation for 7pm.",
		"The house is on Elm Street.",
		"",
	} {
		if replyClaimsAttachment(plain) {
			t.Errorf("ordinary text read as a delivery claim: %q", plain)
		}
	}
}

// Handing over the recent-image ids BEFORE the model goes looking for them.
//
// The failure this closes: asked to add someone to "the picture of me wasting
// away in the garage", the turn listed the workspace, found dozens of
// edit-<id>.png names with nothing to tell them apart, said "there are too many
// files", guessed at one, and ended promising work it never did. The ids and
// their captions existed the whole time — nothing put them in front of it.

// notePNG is a real, decodable image — the space verifies what it stores.
func notePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// noteSession builds a session whose image space holds the given captions,
// oldest first — so the last one listed becomes image#1.
func noteSession(t *testing.T, captions ...string) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "craig", AgentID: "wiwee", WorkspaceDir: t.TempDir()}
	for _, c := range captions {
		if ref := RecordRecentImage(sess, notePNG(t), c, ImageFromUser); ref == "" {
			t.Fatalf("image space unavailable: %q", c)
		}
	}
	return sess
}

func TestTheManifestArrivesWhenTheUserNamesAPicture(t *testing.T) {
	sess := noteSession(t, "received: Alex portrait", "edited: me wasting away in the garage")

	note := imageSpaceNote(sess, "Add Alex to the picture of me wasting away in the garage")
	if note == "" {
		t.Fatal("a request naming a picture must get the manifest")
	}
	// The ids it can pass, and the captions that tell them apart — the two
	// things a workspace listing of edit-<id>.png names cannot give it.
	if !strings.Contains(note, "image#1") || !strings.Contains(note, "wasting away in the garage") {
		t.Errorf("note must name the ids and what they are:\n%s", note)
	}
	// Marked as the framework speaking. The ids are ours, not something the
	// user typed, and thanking them for it reads as a non sequitur.
	if !strings.Contains(note, "FRAMEWORK NOTE") {
		t.Errorf("note must be tagged as framework context:\n%s", note)
	}
}

func TestThePictureTriggerIsTheUsersOwnWords(t *testing.T) {
	sess := noteSession(t, "edited: a cat")

	// A request to change an existing image has to say what to change, so
	// unlike a model writing a caption, the user's words are reliable here.
	for _, ask := range []string{
		"Add Alex to the picture of me in the garage",
		"Blend these so the guy in the second photo is on the hood",
		"can you photoshop him out",
		"send me that meme again",
		"make the screenshot bigger",
		"Do you still have those pics?",
	} {
		if imageSpaceNote(sess, ask) == "" {
			t.Errorf("a request about a picture must get the manifest: %q", ask)
		}
	}

	// And ordinary conversation must not. The note is cheap, not free, and a
	// picture list on a turn about dinner is noise that invites the model to
	// reference something nobody asked about.
	for _, ask := range []string{
		"what's the weather looking like tomorrow",
		"remind me to call the plumber",
		"that was an epic fail",
		"she gifted me a bottle of wine",
		"summarize the last three emails",
	} {
		if got := imageSpaceNote(sess, ask); got != "" {
			t.Errorf("ordinary talk must not carry the manifest: %q →\n%s", ask, got)
		}
	}
}

func TestAnEmptySpaceSaysNothing(t *testing.T) {
	sess := noteSession(t) // no images recorded
	if got := imageSpaceNote(sess, "send me that picture again"); got != "" {
		t.Errorf("nothing to list means nothing to say, got:\n%s", got)
	}
	if got := imageSpaceNote(nil, "send me that picture again"); got != "" {
		t.Errorf("a nil session must be silent, got:\n%s", got)
	}
}

// A song attached to a message rides the media list, which the transport sends
// as a file; an image stays an image. The collector gathered images only, so an
// MP3 went out as phantom-img-*.jpg and a video was dropped outright.
func TestAnAttachedSongIsNotSentAsAnImage(t *testing.T) {
	ws := t.TempDir()
	for name, body := range map[string]string{"song.mp3": "ID3song", "pic.png": "\x89PNGpic", "clip.mp4": "clip"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sess := &ToolSession{WorkspaceDir: ws}
	images, videos := messageMedia(sess, map[string]any{"attachments": []any{"song.mp3", "pic.png", "clip.mp4"}}, "here it is")
	if len(images) != 1 || len(videos) != 2 {
		t.Fatalf("want 1 image and 2 media files (song + clip), got %d images, %d media", len(images), len(videos))
	}
	images, videos = messageMedia(sess, map[string]any{"attachment": "song.mp3"}, "")
	if len(images) != 0 || len(videos) != 1 {
		t.Errorf("the singular alias routes the same way: %d images, %d media", len(images), len(videos))
	}
}

// A reply that promises a file it never made. Observed as exactly
// "[ATTACH: find-dkfindcraig.jpg]" — a filename with the right shape and the
// subject's name stuffed into it, for a picture the turn never produced (it
// called no tools at all).

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

func scheduleHomeSession(t *testing.T) *ToolSession {
	t.Helper()
	return &ToolSession{Username: "craig@example.com", DB: &DBase{Store: kvlite.MemStore()}}
}

// THE BUG. Builder holds the operator toolset on purpose, so that "build me a
// tool and run it every 30 minutes" is one job rather than a build plus a
// handoff. But a schedule it created reported back to Builder, in the build
// session that created it: it ran correctly and filed its findings in a
// workshop nobody reopens, which reads exactly like the schedule going to the
// wrong agent.
func TestAnAuthorHandsAScheduleToTheAgentThatRunsIt(t *testing.T) {
	sess := scheduleHomeSession(t)
	if got := scheduleHomeAgent(sess, "seed-builder", "agent-weather"); got != "agent-weather" {
		t.Errorf("Builder kept a schedule it built for another agent: home=%q", got)
	}
	// A controller keeps its own: delegating and then reading the results is
	// what a Fleet agent is for.
	if got := scheduleHomeAgent(sess, "seed-chat", "agent-weather"); got != "seed-chat" {
		t.Errorf("a controller lost the schedule it manages: home=%q", got)
	}
	// An agent scheduling itself is unaffected, whoever it is.
	if got := scheduleHomeAgent(sess, "seed-builder", "seed-builder"); got != "seed-builder" {
		t.Errorf("self-scheduling was rerouted: home=%q", got)
	}
	// A schedule with no runner agent (a pipeline or a machine) has nowhere
	// else to go, so it stays with whoever set it up.
	if got := scheduleHomeAgent(sess, "seed-builder", ""); got != "seed-builder" {
		t.Errorf("a pipeline schedule was orphaned: home=%q", got)
	}
}

// Authoring is Builder's alone since the Author flag retired, so an agent that
// still carries the flag is an ordinary agent here: it keeps its schedules.
func TestTheHandoffFollowsTheCapabilityNotTheName(t *testing.T) {
	sess := scheduleHomeSession(t)
	author, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Author", OrchestratorPrompt: "You author.", Author: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := scheduleHomeAgent(sess, author.ID, "agent-weather"); got != author.ID {
		t.Errorf("the retired flag still handed a schedule off: home=%q", got)
	}

	both, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Conductor", OrchestratorPrompt: "You conduct.", Author: true, Fleet: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := scheduleHomeAgent(sess, both.ID, "agent-weather"); got != both.ID {
		t.Errorf("a fleet controller lost its schedule: home=%q", got)
	}

	// A plain agent keeps its own schedules.
	plain, _ := saveAgent(sess.DB, AgentRecord{Owner: sess.Username, Name: "Plain", OrchestratorPrompt: "You help."})
	if got := scheduleHomeAgent(sess, plain.ID, "agent-weather"); got != plain.ID {
		t.Errorf("a plain agent's schedule was rerouted: home=%q", got)
	}
}

// A monitor has no run target: its whole shape is "wake me when this happens".
// So an author has to say whose monitor it is, and is TOLD so rather than left
// to find out when the alert never arrives.
func TestAnAuthorMustSayWhichAgentAMonitorWakes(t *testing.T) {
	sess := scheduleHomeSession(t)

	_, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyChannel, "")
	if err == nil {
		t.Fatal("Builder was allowed to point a monitor at its own build session")
	}
	for _, want := range []string{"wake_agent", "nobody will see it", "notify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// Delivered straight to a person or a chat: nothing has to wake up for it
	// to work, so there is nothing to refuse.
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyDirect, ""); err != nil {
		t.Errorf("a direct-delivery monitor was refused: %v", err)
	}
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "", EventNotifyChannel, "chat-123"); err != nil {
		t.Errorf("a monitor delivering to a chat was refused: %v", err)
	}

	// Any other agent keeps the old behavior: it wakes itself.
	if got, err := resolveMonitorWakeAgent(sess, "seed-chat", "", EventNotifyChannel, ""); err != nil || got != "seed-chat" {
		t.Errorf("a controller's own monitor changed: %q %v", got, err)
	}
}

// And naming an agent resolves it, by name as well as id, because that is what
// the model has to hand.
func TestWakeAgentResolvesByName(t *testing.T) {
	sess := scheduleHomeSession(t)
	target, err := saveAgent(sess.DB, AgentRecord{
		Owner: sess.Username, Name: "Weather", OrchestratorPrompt: "You watch the weather.",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := resolveMonitorWakeAgent(sess, "seed-builder", "Weather", EventNotifyChannel, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != target.ID {
		t.Errorf("wake agent = %q, want %q", got, target.ID)
	}
	// A name that does not exist is an error naming the fix, not a silent
	// fallback to waking the author.
	if _, err := resolveMonitorWakeAgent(sess, "seed-builder", "Nobody", EventNotifyChannel, ""); err == nil {
		t.Error("an unknown wake_agent was accepted")
	} else if !strings.Contains(err.Error(), "create_agent") {
		t.Errorf("the error does not say what to do: %v", err)
	}
}

// recurring(schedule) puts THIS agent on a clock, with no parameter to point
// it elsewhere. Asked to schedule something, Builder would schedule ITSELF to
// replay a build prompt forever, and the agent the user was talking about
// would have nothing on it at all. Refusing and naming the right tool beats
// being wrong once a minute.
func TestAnAuthorCannotPutItselfOnAClock(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, session: &ChatSession{ID: "s1"}}
	_, err := turn.recurringSchedule(map[string]any{"prompt": "check the thing", "pattern": "hourly"})
	if err == nil {
		t.Fatal("Builder scheduled itself to replay a build prompt on a clock")
	}
	if !strings.Contains(err.Error(), `when="every"`) || !strings.Contains(err.Error(), "agent set to") {
		t.Errorf("the refusal does not name the tool that does this properly: %v", err)
	}

	// Every other agent keeps its own recurring tasks: "remind me in an hour"
	// is exactly this tool's job.
	plain := &chatTurn{agent: AgentRecord{ID: "a1", Name: "Helper"}, session: &ChatSession{ID: "s1"}}
	if _, err := plain.recurringSchedule(map[string]any{"prompt": "check the thing", "pattern": "hourly"}); err != nil {
		if strings.Contains(err.Error(), `when="every"`) {
			t.Errorf("an ordinary agent was refused its own recurring task: %v", err)
		}
	}
}

// The sandbox writes the workspace, so a symlink in it must not have the host
// read what it points at and send it out as an attachment.

func TestAWorkspaceSymlinkIsNotFollowedOut(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-secret.txt")
	_ = os.WriteFile(outside, []byte("host secret"), 0600)
	_ = os.WriteFile(filepath.Join(ws, "chart.png"), []byte("png bytes"), 0600)
	if err := os.Symlink(outside, filepath.Join(ws, "innocent.png")); err != nil {
		t.Skip("symlinks unavailable")
	}
	sess := &ToolSession{WorkspaceDir: ws}
	if got := resolveWorkspaceImages(sess, []string{"innocent.png", "../x", "/etc/passwd"}); len(got) != 0 {
		t.Fatalf("an escape was read: %d item(s)", len(got))
	}
	if got := resolveWorkspaceImages(sess, []string{"chart.png"}); len(got) != 1 {
		t.Fatal("an ordinary workspace file should still resolve")
	}
}
