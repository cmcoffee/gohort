package core

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A picture the agent made is not evidence of what anything looks like.
//
// Reference images existed with no notion of provenance, so an agent could keep
// its own render and later treat it as the reference for the subject it had
// invented — each reuse compounding the invention, the depicted thing drifting
// further from the real one. These pin that origin is recorded at every
// producer, survives a keep, and cannot be laundered by re-keeping.
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

// A burst of renders must not expire the photo they were attempts AT. This is
// the reported failure: the user's selfie aged out behind the agent's own
// output, and the agent asked them to send it again.
func TestRendersCannotEvictTheUsersPictures(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "received from craig", ImageFromUser) == "" {
		t.Fatal("recording the user's photo failed")
	}
	// Well past the limit, which under one flat queue would have evicted it.
	for i := 0; i < recentImageLimit*2; i++ {
		if RecordRecentImage(sess, testPNG(t, 4+i%4, 4+i%4), "generated: attempt", ImageFromGenerated) == "" {
			t.Fatalf("recording render %d failed", i)
		}
	}
	var survived bool
	for _, r := range RecentImages(sess) {
		if r.Origin == ImageFromUser {
			survived = true
		}
	}
	if !survived {
		t.Error("the user's photo must outlive any number of the agent's own renders")
	}
	// And the agent's own queue is still bounded, or this just leaks.
	made := 0
	for _, r := range RecentImages(sess) {
		if r.Origin.AgentMade() {
			made++
		}
	}
	if made > recentImageLimit {
		t.Errorf("agent-made queue should stay bounded at %d, got %d", recentImageLimit, made)
	}
}

// The user's own queue is bounded too — protection, not an unbounded store.
func TestSourceQueueIsBounded(t *testing.T) {
	sess := imageSpaceSession(t)
	for i := 0; i < sourceImageLimit*2; i++ {
		if RecordRecentImage(sess, testPNG(t, 4+i%4, 4+i%4), "received from craig", ImageFromUser) == "" {
			t.Fatalf("recording photo %d failed", i)
		}
	}
	if got := len(RecentImages(sess)); got > sourceImageLimit {
		t.Errorf("source queue should cap at %d, got %d", sourceImageLimit, got)
	}
}

// "Keep the picture I just sent you" has to work on the ref the model was
// handed for it. media#N is a separate namespace the edit path accepted and
// keep did not.
func TestKeepAcceptsAMediaRef(t *testing.T) {
	sess := imageSpaceSession(t)
	sess.RegisterInboundMedia("image", testPNG(t, 8, 8), "craig")

	kept, err := KeepImage(sess, "media#1", "wren", "the user's dog")
	if err != nil {
		t.Fatalf("keeping an attached photo should work: %v", err)
	}
	if kept.Origin != ImageFromUser {
		t.Errorf("an attachment is user-origin by definition, got %q", kept.Origin)
	}
	if _, ok := ResolveKeptImage(sess, "image#wren"); !ok {
		t.Error("the kept photo should resolve under its lasting name")
	}
}

// The ring manifest is where a photo somebody sent lives before anyone keeps
// it, and it used to list everything flat — so a render and a real photograph
// differed only by the prose in their notes. Asked for a reference, the model
// took whatever was newest, which after two attempts is always something it
// made itself.
func TestRingManifestSeparatesGivenFromMade(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "received from craig", ImageFromUser) == "" {
		t.Fatal("record failed")
	}
	if RecordRecentImage(sess, testPNG(t, 9, 9), "generated: a dog", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	m := RecentImageManifest(sess)

	given := strings.Index(m, "GIVEN or found")
	made := strings.Index(m, "YOU MADE")
	if given < 0 || made < 0 {
		t.Fatalf("the manifest should separate provenance, got:\n%s", m)
	}
	// Given first: those are what a request for a reference means, and the
	// newest-first flat list put the latest render at the top instead.
	if given > made {
		t.Errorf("pictures you were given should lead, got:\n%s", m)
	}
	if !strings.Contains(m, "not evidence of what anything really looks like") {
		t.Errorf("the agent's own output should be marked, got:\n%s", m)
	}
	// And the instruction must point at the parameter, not the removed action.
	if strings.Contains(m, `action="edit"`) {
		t.Errorf("the manifest should not name a removed action, got:\n%s", m)
	}
}

// A ring holding only renders must not print an empty "given" heading, and vice
// versa — a heading over nothing reads as missing data.
func TestRingManifestOmitsEmptyGroups(t *testing.T) {
	sess := imageSpaceSession(t)
	if RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a dog", ImageFromGenerated) == "" {
		t.Fatal("record failed")
	}
	m := RecentImageManifest(sess)
	if strings.Contains(m, "GIVEN or found") {
		t.Errorf("nothing was given; that heading should be absent, got:\n%s", m)
	}
	if !strings.Contains(m, "YOU MADE") {
		t.Errorf("the render should still be listed, got:\n%s", m)
	}
}

// image#N is a POSITION, and every save renumbers the ring. The gap this covers
// is between the model CHOOSING a ref and the call USING it: a render saved in
// between slides into image#1 and pushes everything the model was looking at
// down one, so the call lands on a picture nobody asked about. Delivered, that
// is a reply carrying two pictures — the right one, and one from an earlier
// request entirely.

// freezeSession builds a session with its own ring, and a snapshot already
// taken — the state a tool round starts in.
func freezeSession(t *testing.T, user, agent string) *ToolSession {
	t.Helper()
	return &ToolSession{Username: user, AgentID: agent}
}

func TestPositionalRefSurvivesASaveInTheSameRound(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")

	RecordRecentImage(sess, []byte("THE-PHOTO-SHE-SENT"), "a photo", ImageFromUser)
	// The model reads the round's tools with this ring in front of it.
	SnapshotImageRefs(sess)

	args := map[string]any{"path": "image#1"}
	// A sibling render lands mid-round and takes over image#1.
	RecordRecentImage(sess, []byte("A-RENDER"), "generated: something else", ImageFromGenerated)

	if n := FreezeImageRefs(sess, args); n != 1 {
		t.Fatalf("froze %d refs, want 1", n)
	}
	got, ok := ResolveRecentImage(sess, args["path"].(string))
	if !ok {
		t.Fatalf("frozen ref %q no longer resolves", args["path"])
	}
	if !bytes.Equal(got, []byte("THE-PHOTO-SHE-SENT")) {
		t.Errorf("image#1 delivered the render that arrived after the model chose it: %q", got)
	}
	// Live resolution is what the freeze exists to bypass — pinned here so the
	// test fails if the ring ever stops renumbering and this stops proving
	// anything.
	live, _ := ResolveRecentImage(sess, "image#1")
	if bytes.Equal(live, []byte("THE-PHOTO-SHE-SENT")) {
		t.Fatal("the ring did not renumber; this test no longer covers the failure")
	}
}

func TestFreezeRewritesEveryRefInAList(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("OLDEST"), "a photo", ImageFromUser)
	RecordRecentImage(sess, []byte("MIDDLE"), "another photo", ImageFromUser)
	SnapshotImageRefs(sess)

	args := map[string]any{"images": []any{"image#1", "image#2"}}
	RecordRecentImage(sess, []byte("LATE-RENDER"), "generated", ImageFromGenerated)

	if n := FreezeImageRefs(sess, args); n != 2 {
		t.Fatalf("froze %d refs, want 2", n)
	}
	list := args["images"].([]any)
	first, _ := ResolveRecentImage(sess, list[0].(string))
	second, _ := ResolveRecentImage(sess, list[1].(string))
	if !bytes.Equal(first, []byte("MIDDLE")) || !bytes.Equal(second, []byte("OLDEST")) {
		t.Errorf("list resolved to %q and %q, want MIDDLE and OLDEST", first, second)
	}
}

func TestFreezeLeavesProseAlone(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)

	// A prompt MENTIONING a ref is the model describing a picture, not
	// addressing one. Rewriting inside it would put a machine id in a sentence
	// a user reads.
	prompt := "make it look like image#1 but at night"
	args := map[string]any{"prompt": prompt}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote %d refs inside prose", n)
	}
	if args["prompt"] != prompt {
		t.Errorf("prompt was rewritten to %q", args["prompt"])
	}
}

func TestFreezeLeavesStableAndKeptRefsAlone(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)
	all := RecentImages(sess)
	if len(all) != 1 || all[0].ID == "" {
		t.Fatalf("expected one picture with a stable id, got %+v", all)
	}

	args := map[string]any{"a": all[0].ID, "b": "image#brand_mark", "c": "not a ref"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote %d refs that were already durable", n)
	}
	if args["a"] != all[0].ID || args["b"] != "image#brand_mark" || args["c"] != "not a ref" {
		t.Errorf("args were altered: %+v", args)
	}
}

func TestWithoutASnapshotPositionsResolveLive(t *testing.T) {
	// A caller that never wires the hook keeps the old behavior rather than
	// silently resolving against an empty snapshot and failing to find
	// anything.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)

	args := map[string]any{"path": "image#1"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Fatalf("froze %d refs with no snapshot taken", n)
	}
	got, ok := ResolveRecentImage(sess, args["path"].(string))
	if !ok || !bytes.Equal(got, []byte("A-PHOTO")) {
		t.Errorf("unfrozen ref stopped resolving: %q ok=%v", got, ok)
	}
}

func TestSnapshotIsRetakenEachRound(t *testing.T) {
	// The freeze must not pin a ref FOREVER: once the model has seen the
	// results naming the new picture, image#1 legitimately means that one.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("FIRST"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)
	RecordRecentImage(sess, []byte("SECOND"), "generated", ImageFromGenerated)

	// Next round: the model has read the result announcing SECOND.
	SnapshotImageRefs(sess)
	args := map[string]any{"path": "image#1"}
	if n := FreezeImageRefs(sess, args); n != 1 {
		t.Fatalf("froze %d refs, want 1", n)
	}
	got, _ := ResolveRecentImage(sess, args["path"].(string))
	if !bytes.Equal(got, []byte("SECOND")) {
		t.Errorf("image#1 still means the previous round's picture: %q", got)
	}
}

func TestABackgroundRenderDoesNotRenumberUnderTheModel(t *testing.T) {
	// The between-rounds window: a detached render finishes while no round is
	// running, so the next snapshot would otherwise pin a ring the model has
	// never seen — its image#1 silently becomes the render, and every position
	// it is holding moves down one.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("THE-PHOTO-SHE-SENT"), "a photo", ImageFromUser)

	detached := freezeSession(t, "alice", "agent-wren")
	detached.Detached = true
	RecordRecentImage(detached, []byte("BACKGROUND-RENDER"), "generated: something", ImageFromGenerated)

	// A new round begins. The model has read no list naming the render.
	SnapshotImageRefs(sess)
	args := map[string]any{"path": "image#1"}
	FreezeImageRefs(sess, args)
	got, ok := ResolveRecentImage(sess, args["path"].(string))
	if !ok || !bytes.Equal(got, []byte("THE-PHOTO-SHE-SENT")) {
		t.Errorf("image#1 moved to the background render the model was never shown: %q", got)
	}
	// It is still reachable — by the id its result handed over.
	all := RecentImages(sess)
	if len(all) != 2 || !all[0].Unannounced {
		t.Fatalf("expected the render to be present and unannounced, got %+v", all)
	}
	if data, ok := ResolveRecentImage(sess, all[0].ID); !ok || !bytes.Equal(data, []byte("BACKGROUND-RENDER")) {
		t.Errorf("the stable id did not reach the background render: %q ok=%v", data, ok)
	}
}

func TestListingTheRingGivesABackgroundRenderItsPosition(t *testing.T) {
	// Being shown the list IS the announcement: from then on the model holds
	// positions it actually read, so the render takes image#1 like anything
	// else.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("THE-PHOTO-SHE-SENT"), "a photo", ImageFromUser)
	detached := freezeSession(t, "alice", "agent-wren")
	detached.Detached = true
	RecordRecentImage(detached, []byte("BACKGROUND-RENDER"), "generated: something", ImageFromGenerated)

	if m := RecentImageManifest(sess); m == "" {
		t.Fatal("expected a manifest")
	}
	SnapshotImageRefs(sess)
	args := map[string]any{"path": "image#1"}
	FreezeImageRefs(sess, args)
	got, _ := ResolveRecentImage(sess, args["path"].(string))
	if !bytes.Equal(got, []byte("BACKGROUND-RENDER")) {
		t.Errorf("after the ring was listed, image#1 is still %q", got)
	}
	// And the flag is cleared on disk, not just for this read.
	if all := RecentImages(sess); all[0].Unannounced {
		t.Error("the announcement did not persist")
	}
}

func TestAForegroundSaveIsAnnouncedImmediately(t *testing.T) {
	// Only a BACKGROUND save is held back. A foreground one puts its position
	// in a result the model reads on the very next round, so holding it back
	// would make the ring disagree with what the model was just told.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("FIRST"), "a photo", ImageFromUser)
	RecordRecentImage(sess, []byte("SECOND"), "generated", ImageFromGenerated)

	SnapshotImageRefs(sess)
	args := map[string]any{"path": "image#1"}
	FreezeImageRefs(sess, args)
	got, _ := ResolveRecentImage(sess, args["path"].(string))
	if !bytes.Equal(got, []byte("SECOND")) {
		t.Errorf("a foreground save did not take image#1: %q", got)
	}
}

func TestOutOfRangeRefIsLeftForTheNormalError(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)

	args := map[string]any{"path": "image#9"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote a position that does not exist")
	}
	if _, ok := ResolveRecentImage(sess, "image#9"); ok {
		t.Error("image#9 resolved to something")
	}
}

// Whose picture is image#1. The refs are POSITIONAL, so a ring shared across a
// fleet hands an agent asking for "the one you just made" whatever another
// agent made a second earlier — silently, plausibly, and wrong.

func spaceSession(t *testing.T, user, agent string) *ToolSession {
	t.Helper()
	return &ToolSession{Username: user, AgentID: agent}
}

func TestOneAgentsPictureIsNotAnothersImageOne(t *testing.T) {
	attachmentTestDir(t) // scopes ImageDir to a temp dir
	wren := spaceSession(t, "alice", "agent-wren")
	other := spaceSession(t, "alice", "agent-wiwee")

	if ref := RecordRecentImage(wren, []byte("WREN-PICTURE"), "wren made this", ImageFromUser); ref != "image#1" {
		t.Fatalf("record returned %q", ref)
	}
	if ref := RecordRecentImage(other, []byte("OTHER-PICTURE"), "wiwee made this", ImageFromUser); ref != "image#1" {
		t.Fatalf("record returned %q", ref)
	}

	got, ok := ResolveRecentImage(wren, "image#1")
	if !ok {
		t.Fatal("wren should still see its own picture")
	}
	if !bytes.Equal(got, []byte("WREN-PICTURE")) {
		t.Errorf("image#1 for wren resolved to another agent's picture: %q", got)
	}
	got, ok = ResolveRecentImage(other, "image#1")
	if !ok || !bytes.Equal(got, []byte("OTHER-PICTURE")) {
		t.Errorf("image#1 for the other agent resolved to %q", got)
	}
	// Each ring holds only its own.
	if n := len(RecentImages(wren)); n != 1 {
		t.Errorf("wren's ring holds %d, want only its own picture", n)
	}
}

func TestTheSameAgentKeepsOneRingAcrossItsSurfaces(t *testing.T) {
	// Web chat and a phone conversation are the same agent, and "edit the one
	// you just made" has to work across them — including from the wake turn,
	// which runs under a different session id than the task that made it.
	attachmentTestDir(t)
	made := &ToolSession{Username: "alice", AgentID: "agent-wren", ChatSessionID: "web-session"}
	woken := &ToolSession{Username: "alice", AgentID: "agent-wren", ChatSessionID: "scheduled:chan:xyz"}

	RecordRecentImage(made, []byte("THE-EDIT"), "edited", ImageFromEdited)
	got, ok := ResolveRecentImage(woken, "image#1")
	if !ok || !bytes.Equal(got, []byte("THE-EDIT")) {
		t.Errorf("the same agent must reach its own picture from any session, got %q ok=%v", got, ok)
	}
}

func TestASessionWithNoAgentGetsItsOwnRing(t *testing.T) {
	attachmentTestDir(t)
	anon := &ToolSession{Username: "alice"}
	named := spaceSession(t, "alice", "agent-wren")
	RecordRecentImage(named, []byte("AGENT-PICTURE"), "", ImageFromUser)
	RecordRecentImage(anon, []byte("ANON-PICTURE"), "", ImageFromUser)

	got, _ := ResolveRecentImage(anon, "image#1")
	if !bytes.Equal(got, []byte("ANON-PICTURE")) {
		t.Errorf("an agent-less session must not read an agent's ring, got %q", got)
	}
	if n := len(RecentImages(named)); n != 1 {
		t.Errorf("the agent's ring picked up an unrelated image (%d entries)", n)
	}
}

// The image space and the reference forms that resolve through it. The point of
// the space is that the MODEL no longer does filesystem hygiene: it never
// deletes, and an image it made three turns ago is still addressable.

// testPNG is a real, decodable image — verifyInputImage rejects anything that
// isn't, so a []byte("fake") fixture would test the wrong path.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func imageSpaceSession(t *testing.T) *ToolSession {
	t.Helper()
	saved := imageDir
	SetImageDir(t.TempDir())
	t.Cleanup(func() { imageDir = saved })
	return &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
}

func TestRecentImageRoundTrip(t *testing.T) {
	sess := imageSpaceSession(t)
	data := testPNG(t, 8, 8)
	if ref := RecordRecentImage(sess, data, "generated: a cat", ImageFromGenerated); ref != "image#1" {
		t.Fatalf("ref = %q, want image#1", ref)
	}
	got, ok := ResolveRecentImage(sess, "image#1")
	if !ok {
		t.Fatal("image#1 must resolve right after being recorded")
	}
	if !bytes.Equal(got, data) {
		t.Error("resolved bytes differ from what was recorded")
	}
	all := RecentImages(sess)
	if len(all) != 1 || all[0].Note != "generated: a cat" {
		t.Errorf("recent = %+v, want the note preserved", all)
	}
}

func TestNewestIsAlwaysImageOne(t *testing.T) {
	// Positional refs: "the one you just made" is always image#1, so the model
	// never has to track which number a picture was.
	sess := imageSpaceSession(t)
	first := testPNG(t, 8, 8)
	second := testPNG(t, 16, 16)
	RecordRecentImage(sess, first, "first", ImageFromUser)
	RecordRecentImage(sess, second, "second", ImageFromUser)

	all := RecentImages(sess)
	if len(all) != 2 {
		t.Fatalf("recent = %d entries, want 2", len(all))
	}
	if all[0].Note != "second" || all[1].Note != "first" {
		t.Errorf("order = [%s %s], want newest first", all[0].Note, all[1].Note)
	}
	got, _ := ResolveRecentImage(sess, "image#1")
	if !bytes.Equal(got, second) {
		t.Error("image#1 must be the most recent image")
	}
	got, _ = ResolveRecentImage(sess, "image#2")
	if !bytes.Equal(got, first) {
		t.Error("image#2 must be the one before it")
	}
}

func TestSpaceCollectsItsOwnGarbage(t *testing.T) {
	// The whole reason this exists: the model used to be told to pass
	// cleanup=true and would forget. Pruning is the framework's job now.
	sess := imageSpaceSession(t)
	for i := 0; i < recentImageLimit+5; i++ {
		RecordRecentImage(sess, testPNG(t, 8+i, 8), "img", ImageFromUser)
	}
	if got := len(RecentImages(sess)); got != recentImageLimit {
		t.Errorf("space holds %d, want it pruned to %d", got, recentImageLimit)
	}
	// Pruning must take the sidecars with it, not orphan them.
	entries, err := os.ReadDir(recentImageDir(sess))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != recentImageLimit*2 {
		t.Errorf("%d files on disk, want %d (image + sidecar each)", len(entries), recentImageLimit*2)
	}
}

func TestSpaceIsPerUser(t *testing.T) {
	// One user's pictures must never be addressable by another.
	saved := imageDir
	SetImageDir(t.TempDir())
	t.Cleanup(func() { imageDir = saved })

	alice := &ToolSession{Username: "alice"}
	bob := &ToolSession{Username: "bob"}
	RecordRecentImage(alice, testPNG(t, 8, 8), "alice's", ImageFromUser)
	if len(RecentImages(bob)) != 0 {
		t.Error("another user's image must not appear in this user's space")
	}
	if _, ok := ResolveRecentImage(bob, "image#1"); ok {
		t.Error("another user's image must not resolve")
	}
}

func TestSpaceNeedsAUserAndFailsSoft(t *testing.T) {
	// No username means no space. That's a missing convenience, never an error
	// — image generation still has to work.
	anon := &ToolSession{}
	if ref := RecordRecentImage(anon, testPNG(t, 8, 8), "x", ImageFromUser); ref != "" {
		t.Errorf("ref = %q, want empty for a session with no user", ref)
	}
	if RecentImages(anon) != nil {
		t.Error("an anonymous session has no space")
	}
	if RecentImageManifest(anon) != "" {
		t.Error("an empty space renders no manifest")
	}
	if ref := RecordRecentImage(nil, testPNG(t, 8, 8), "x", ImageFromUser); ref != "" {
		t.Errorf("ref = %q, want empty for a nil session", ref)
	}
}

func TestUsernameCannotEscapeTheSpaceDirectory(t *testing.T) {
	// Both path segments are attacker-shaped — a username and an agent id — and
	// each is reduced to one safe element. The ring lives at
	// recent/<user>/<agent>, so the check is that it stays exactly two levels
	// under the root with no traversal in either.
	sess := imageSpaceSession(t)
	sess.Username = "../../etc"
	sess.AgentID = "../../../root"
	dir := recentImageDir(sess)
	if strings.Contains(dir, "..") {
		t.Errorf("directory %q escapes the image space", dir)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(dir))) != "recent" {
		t.Errorf("directory %q is not under the recent/ root", dir)
	}
}

func TestManifestNamesEveryImage(t *testing.T) {
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a cat", ImageFromGenerated)
	RecordRecentImage(sess, testPNG(t, 9, 9), "edited image#1: snowy", ImageFromEdited)

	m := RecentImageManifest(sess)
	for _, want := range []string{"image#1", "image#2", "generated: a cat", "edited image#1: snowy"} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest missing %q:\n%s", want, m)
		}
	}
	// The model should stop trying to clean up after itself.
	if !strings.Contains(m, "never need to delete") {
		t.Errorf("manifest should say the space is managed:\n%s", m)
	}
}

func TestResolveInputImageAcceptsSpaceAndWorkspace(t *testing.T) {
	sess := imageSpaceSession(t)
	data := testPNG(t, 8, 8)
	RecordRecentImage(sess, data, "one", ImageFromUser)

	got, err := resolveInputImage(sess, "image#1")
	if err != nil {
		t.Fatalf("image#1 must resolve as a source: %v", err)
	}
	if !bytes.Equal(got.data, data) {
		t.Error("resolved the wrong bytes for image#1")
	}

	if err := os.WriteFile(filepath.Join(sess.WorkspaceDir, "photo.png"), data, 0600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if _, err := resolveInputImage(sess, "photo.png"); err != nil {
		t.Errorf("a workspace filename must resolve as a source: %v", err)
	}
}

func TestResolveInputImageRefusesURLs(t *testing.T) {
	// Fetching an arbitrary URL here would be SSRF through a dispatch scoped to
	// the backend's own host. The refusal names the tool that CAN do it.
	sess := imageSpaceSession(t)
	_, err := resolveInputImage(sess, "https://example.com/cat.png")
	if err == nil {
		t.Fatal("a URL must not be fetched as a source image")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("error should point at the fetch action: %v", err)
	}
}

func TestResolveInputImageRejectsNonImages(t *testing.T) {
	// A text file renamed .png would upload cleanly and fail deep inside the
	// backend with something unreadable.
	sess := imageSpaceSession(t)
	if err := os.WriteFile(filepath.Join(sess.WorkspaceDir, "notreally.png"), []byte("hello"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := resolveInputImage(sess, "notreally.png"); err == nil {
		t.Fatal("non-image bytes must be rejected before upload")
	}
}

func TestResolveInputImageRejectsEscapes(t *testing.T) {
	sess := imageSpaceSession(t)
	for _, ref := range []string{"../outside.png", "/etc/passwd"} {
		if _, err := resolveInputImage(sess, ref); err == nil {
			t.Errorf("%q must not resolve outside the workspace", ref)
		}
	}
}

func TestMissingSpaceRefExplainsItself(t *testing.T) {
	// A stale id has to say what to do, or the model retries the same call.
	sess := imageSpaceSession(t)
	_, err := resolveInputImage(sess, "image#7")
	if err == nil {
		t.Fatal("an out-of-range image id must fail")
	}
	if !strings.Contains(err.Error(), "help") {
		t.Errorf("error should point at the listing: %v", err)
	}
}

func TestResolveInputImagesPreservesOrderAndCap(t *testing.T) {
	sess := imageSpaceSession(t)
	first := testPNG(t, 8, 8)
	second := testPNG(t, 16, 16)
	RecordRecentImage(sess, first, "first", ImageFromUser)
	RecordRecentImage(sess, second, "second", ImageFromUser)

	// image#1 is newest (second), image#2 is first — order must be preserved as
	// GIVEN, since it decides subject vs background in a compose.
	got, err := resolveInputImages(sess, []string{"image#2", "image#1"}, 2)
	if err != nil {
		t.Fatalf("resolveInputImages: %v", err)
	}
	if len(got) != 2 || !bytes.Equal(got[0].data, first) || !bytes.Equal(got[1].data, second) {
		t.Error("caller order was not preserved")
	}
	if _, err := resolveInputImages(sess, []string{"image#1", "image#2"}, 1); err == nil {
		t.Fatal("more images than the backend takes must be refused, not truncated")
	}
}

func TestInboundPhotosJoinTheSpace(t *testing.T) {
	// "Blend the two photos I just sent" in a channel picked the last two
	// images the assistant had GENERATED instead. media#N is turn-scoped and
	// the space only held produced images, so the two id schemes described
	// different pictures and the model reached for the wrong one.
	sess := imageSpaceSession(t)
	first := testPNG(t, 8, 8)
	second := testPNG(t, 16, 16)

	if id := sess.RegisterInboundMedia("image", first, "Alice"); id != "media#1" {
		t.Fatalf("first media id = %q, want media#1", id)
	}
	if id := sess.RegisterInboundMedia("image", second, "Alice"); id != "media#2" {
		t.Fatalf("second media id = %q, want media#2", id)
	}

	// media#N stays ARRIVAL order — media#1 is the first photo sent, which is
	// what "the first one" means to the person who sent them.
	b64, _, ok := sess.ResolveInboundMedia("media#1")
	if !ok {
		t.Fatal("media#1 must resolve")
	}
	got, err := decodeBase64Image(b64)
	if err != nil || !bytes.Equal(got, first) {
		t.Error("media#1 must be the FIRST photo sent")
	}

	// ...and both are in the space, so a later turn can still edit them.
	all := RecentImages(sess)
	if len(all) != 2 {
		t.Fatalf("space holds %d, want both inbound photos", len(all))
	}
	if !strings.Contains(all[0].Note, "Alice") {
		t.Errorf("note = %q, should attribute the sender", all[0].Note)
	}
	// Space order is newest-first, the opposite of media order — which is why
	// the param description sends the model to media#N for this-turn photos.
	spaceNewest, ok := ResolveRecentImage(sess, "image#1")
	if !ok || !bytes.Equal(spaceNewest, second) {
		t.Error("image#1 must be the most recently received photo")
	}
}

func TestInboundVideoDoesNotJoinTheImageSpace(t *testing.T) {
	sess := imageSpaceSession(t)
	sess.RegisterInboundMedia("video", testPNG(t, 8, 8), "Alice")
	if got := len(RecentImages(sess)); got != 0 {
		t.Errorf("space holds %d, want 0 — a clip is not an editable image", got)
	}
}

func TestExpiredMediaRefPointsAtTheLastingOne(t *testing.T) {
	// "The photo expired from memory after my last attempt." media#N is
	// turn-scoped by construction, but the picture was copied into the space
	// when it arrived — so it was never lost, and telling the model to ask for
	// it again ends a conversation that could have continued.
	sess := imageSpaceSession(t)
	sess.RegisterInboundMedia("image", testPNG(t, 8, 8), "")

	// A later turn: fresh session, same user, so media#1 is gone but the space
	// is not.
	later := &ToolSession{Username: sess.Username, WorkspaceDir: t.TempDir()}
	_, err := resolveInputImage(later, "media#1")
	if err == nil {
		t.Fatal("a this-turn id must not resolve on a later turn")
	}
	if !strings.Contains(err.Error(), "image#1") {
		t.Errorf("the error must name the lasting handle:\n%v", err)
	}
	if strings.Contains(err.Error(), "re-attach") {
		t.Errorf("must not ask for the photo again when it is still here:\n%v", err)
	}
	// And that handle has to actually work.
	if _, err := resolveInputImage(later, "image#1"); err != nil {
		t.Errorf("the suggested handle must resolve: %v", err)
	}
}

func TestMediaRefForMediaThatNeverArrivedIsNotCalledExpired(t *testing.T) {
	// The agent found two pictures in one turn, then tried to blend them as
	// media#1 and media#2. Nothing had been attached to that message at all —
	// media#N was simply the wrong namespace for pictures IT had downloaded.
	// The error answered "this is a later turn, so it no longer resolves", and
	// the agent duly told the user the image ids had expired in the queue: a
	// confident, specific account of something that never happened.
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "found: trump", ImageFromUser)
	RecordRecentImage(sess, testPNG(t, 16, 16), "found: shazz", ImageFromUser)

	_, err := resolveInputImage(sess, "media#1")
	if err == nil {
		t.Fatal("a media id must not resolve when no media arrived")
	}
	msg := err.Error()
	for _, banned := range []string{"later turn", "expire"} {
		if strings.Contains(msg, banned) {
			t.Errorf("nothing expired — the error must not say %q:\n%v", banned, err)
		}
	}
	// It has to name the namespace the model actually wanted, and hand over
	// the handles that do work.
	if !strings.Contains(msg, "image#1") || !strings.Contains(msg, "image#2") {
		t.Errorf("the error must list the lasting handles it could have used:\n%v", err)
	}
}

func TestOutOfRangeMediaRefReportsTheCount(t *testing.T) {
	// One photo attached, model asks for the second. That IS an off-by-one,
	// not a wrong namespace, and the count is what tells it so.
	sess := imageSpaceSession(t)
	sess.RegisterInboundMedia("image", testPNG(t, 8, 8), "Alice")

	_, err := resolveInputImage(sess, "media#2")
	if err == nil {
		t.Fatal("an out-of-range media id must fail")
	}
	if !strings.Contains(err.Error(), "media#1") {
		t.Errorf("the error must say where the ids stop:\n%v", err)
	}
}

func TestExpiredMediaWithNothingKeptStillExplains(t *testing.T) {
	// No space (no username, or nothing recorded) — then asking is the only
	// option left, and the message should say so rather than name ids that
	// don't exist.
	sess := &ToolSession{WorkspaceDir: t.TempDir()}
	_, err := resolveInputImage(sess, "media#2")
	if err == nil {
		t.Fatal("an unresolvable media id must fail")
	}
	if !strings.Contains(err.Error(), "re-attach") {
		t.Errorf("with nothing kept, the error should ask for the photo:\n%v", err)
	}
}

func TestAStaleFilenameHandsBackTheManifest(t *testing.T) {
	// The dead end that sent a turn guessing. Every other unresolvable ref in
	// resolveInputImage offers a way forward; a workspace filename — the handle
	// the tool description calls the most direct there is, and the one the
	// framework itself prunes behind — said only that it wasn't there.
	//
	// Observed: an edit reached for a filename from an earlier turn, could not
	// tell a pruned picture from a misremembered one, listed the workspace,
	// found dozens of edit-<id>.png names with nothing to tell them apart, and
	// ended the turn promising work it never did.
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "edited: me wasting away in the garage", ImageFromUser)

	_, err := resolveInputImage(sess, "edit-dketc9x2v8z7.png")
	if err == nil {
		t.Fatal("a filename that isn't there must not resolve")
	}
	msg := err.Error()
	// The picture it wanted, by an id it can actually pass.
	if !strings.Contains(msg, "image#1") || !strings.Contains(msg, "wasting away in the garage") {
		t.Errorf("the error must hand back the manifest:\n%s", msg)
	}
	// And the rule that explains the disappearance, so it isn't a mystery.
	if !strings.Contains(msg, "pruned") {
		t.Errorf("the error must say why the filename stopped working:\n%s", msg)
	}
}

func TestAStaleFilenameWithAnEmptySpaceSaysSo(t *testing.T) {
	// Nothing to offer. Say the picture is gone and name the two real options,
	// rather than implying a list that isn't there.
	sess := imageSpaceSession(t)
	_, err := resolveInputImage(sess, "edit-gone.png")
	if err == nil {
		t.Fatal("a filename that isn't there must not resolve")
	}
	msg := err.Error()
	if strings.Contains(msg, "image#") {
		t.Errorf("must not point at an empty space:\n%s", msg)
	}
	if !strings.Contains(msg, "gone") {
		t.Errorf("must say the picture is gone:\n%s", msg)
	}
}

// --- describe on record ------------------------------------------------------

// captionInline makes the record-time vision pass synchronous for the duration
// of a test. Async by design in production — it must never be on the caller's
// path — which is exactly what makes it race an assertion.
func captionInline(t *testing.T) {
	t.Helper()
	saved := captionRunner
	captionRunner = func(f func()) { f() }
	t.Cleanup(func() { captionRunner = saved })
}

func TestRecordDescribesTheImage(t *testing.T) {
	sess := imageSpaceSession(t)
	captionInline(t)
	sess.LLM = &fakeCaptionLLM{reply: "Six navy bars on a light grid\n\nA bar chart with six navy bars, light grid lines, y-axis in dollars."}
	RecordRecentImage(sess, testPNG(t, 8, 8), "generated: revenue chart", ImageFromGenerated)

	all := RecentImages(sess)
	if len(all) != 1 {
		t.Fatalf("ring holds %d, want 1", len(all))
	}
	if all[0].Caption != "Six navy bars on a light grid" {
		t.Errorf("caption = %q — the ring should know what the picture looks like, not just why it exists", all[0].Caption)
	}
	// The manifest is the answer to "which one did you mean", so it must carry
	// BOTH: why it exists, and what it looks like.
	m := RecentImageManifest(sess)
	if !strings.Contains(m, "generated: revenue chart") || !strings.Contains(m, "Six navy bars") {
		t.Errorf("manifest lost a half:\n%s", m)
	}
}

// The point of describing on record: keeping is then a promotion, not a second
// look at the same picture.
func TestKeepReusesTheRingDescription(t *testing.T) {
	sess := imageSpaceSession(t)
	captionInline(t)
	llm := &countingCaptionLLM{reply: "A logo\n\nA navy circle with a white wordmark."}
	sess.LLM = llm
	RecordRecentImage(sess, testPNG(t, 8, 8), "generated", ImageFromUser)
	if llm.calls != 1 {
		t.Fatalf("record made %d vision calls, want 1", llm.calls)
	}
	kept, err := KeepImage(sess, "image#1", "brand_mark", "")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("keep paid for the image a second time (%d calls) — it should promote what the ring already had", llm.calls)
	}
	if kept.Caption != "A logo" || !strings.Contains(kept.Description, "white wordmark") {
		t.Errorf("promotion lost the description: %+v", kept)
	}
}

// An entry the ring never described (pass off, or still in flight) must still
// get one when it is kept — that is where a description matters most.
func TestKeepDescribesWhenTheRingDidNot(t *testing.T) {
	sess := imageSpaceSession(t)
	saved := CaptionOnRecord
	CaptionOnRecord = false
	t.Cleanup(func() { CaptionOnRecord = saved })

	llm := &countingCaptionLLM{reply: "A chart\n\nSix navy bars."}
	sess.LLM = llm
	RecordRecentImage(sess, testPNG(t, 8, 8), "generated", ImageFromUser)
	if llm.calls != 0 {
		t.Fatalf("record described the image with the pass off")
	}
	kept, err := KeepImage(sess, "image#1", "chart", "")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if llm.calls != 1 || kept.Caption != "A chart" {
		t.Errorf("keep should have described it: calls=%d caption=%q", llm.calls, kept.Caption)
	}
}

// A pruned entry must not have its sidecar resurrected by a caption that was
// still in flight when the prune happened.
func TestCaptionSkipsAPrunedEntry(t *testing.T) {
	sess := imageSpaceSession(t)
	captionInline(t)
	sess.LLM = &fakeCaptionLLM{reply: "Gone\n\nAlready pruned."}
	captionRecentImage(filepath.Join(recentImageDir(sess), "1700000000-deadbeef"), sess, testPNG(t, 8, 8))
	if _, err := os.Stat(filepath.Join(recentImageDir(sess), "1700000000-deadbeef.json")); err == nil {
		t.Error("wrote a sidecar for an image that isn't there")
	}
}

// countingCaptionLLM answers like fakeCaptionLLM and counts the calls.
type countingCaptionLLM struct {
	LLM
	reply string
	calls int
}

func (f *countingCaptionLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	f.calls++
	return &Response{Content: f.reply}, nil
}

// An arriving attachment must NOT be described on record. The turn is about to
// hand that same image to the model, so a background vision call for it is a
// second look at one picture — and on a backend that schedules one call at a
// time it is ahead of the user's own request. This is the "attach a photo and
// ask what it is" hang.
func TestInboundAttachmentIsNotDescribedOnRecord(t *testing.T) {
	sess := imageSpaceSession(t)
	captionInline(t)
	llm := &countingCaptionLLM{reply: "A photo\n\nSomething."}
	sess.LLM = llm
	if id := sess.RegisterInboundMedia("image", testPNG(t, 8, 8), "alice"); id == "" {
		t.Fatal("attachment was not recorded at all")
	}
	if llm.calls != 0 {
		t.Fatalf("an arriving attachment made %d vision calls — it must not compete with the turn that is about to look at it", llm.calls)
	}
	// It IS in the ring, so a later turn can still point at it.
	if len(RecentImages(sess)) != 1 {
		t.Error("the attachment should still join the image space")
	}
}

// The pass runs off the request path with nothing above it, so a panic must not
// reach the process.
func TestCaptionPanicDoesNotEscape(t *testing.T) {
	done := make(chan struct{})
	captionRunner(func() {
		defer close(done)
		panic("boom")
	})
	<-done // if the recover were missing, the test binary would already be gone
}

// The whole point of a stable id: it keeps meaning ONE picture while positions
// slide under it. Saving a render pushes the result to image#1 and moves the
// user's original down, which is how an edit landed on a picture nobody
// mentioned.
func TestStableRingIDSurvivesRenumbering(t *testing.T) {
	sess := imageSpaceSession(t)

	_, original := RecordRecentImageStable(sess, []byte("ORIGINAL"), "received from craig", ImageFromUser)
	if original == "" {
		t.Fatal("a recorded picture must get a stable id")
	}
	if got, ok := ResolveRecentImage(sess, original); !ok || string(got) != "ORIGINAL" {
		t.Fatalf("stable id should resolve to the original, got %q ok=%v", got, ok)
	}

	// Two more saves: the original is now image#3.
	RecordRecentImageStable(sess, []byte("RESULT-1"), "generated", ImageFromGenerated)
	pos, second := RecordRecentImageStable(sess, []byte("RESULT-2"), "generated", ImageFromGenerated)
	if pos != "image#1" {
		t.Errorf("the newest save is image#1, got %q", pos)
	}

	// The position that meant ORIGINAL now means something else...
	if got, _ := ResolveRecentImage(sess, "image#1"); string(got) == "ORIGINAL" {
		t.Error("image#1 should have moved on — the premise of this test is wrong")
	}
	// ...while the stable id still means ORIGINAL.
	if got, ok := ResolveRecentImage(sess, original); !ok || string(got) != "ORIGINAL" {
		t.Errorf("stable id drifted: got %q ok=%v", got, ok)
	}
	if got, ok := ResolveRecentImage(sess, second); !ok || string(got) != "RESULT-2" {
		t.Errorf("second stable id resolves wrong: got %q ok=%v", got, ok)
	}
	if original == second {
		t.Error("two pictures must not share an id")
	}
}

// A dotted id cannot collide with a kept name, because safeKeptName strips
// every character outside [a-z0-9-_]. That is what lets both forms share the
// image# prefix without a reserved word.
func TestStableRingIDCannotCollideWithAKeptName(t *testing.T) {
	sess := imageSpaceSession(t)
	_, stable := RecordRecentImageStable(sess, []byte("PIC"), "received", ImageFromUser)
	if !strings.Contains(stable, ".") {
		t.Fatalf("a ring id must carry the dot that makes it unmistakable: %q", stable)
	}
	if !isRingID(strings.TrimPrefix(stable, RecentImageRefPrefix)) {
		t.Errorf("%q should be recognized as a ring id", stable)
	}
	// A kept-style name must NOT be treated as one.
	if isRingID("brand_mark") {
		t.Error("a kept name must not be mistaken for a ring id")
	}
}

// An id for a picture that has been pruned out must fail loudly rather than
// resolve to whatever is there now — silently working on a different picture is
// the exact failure this exists to prevent.
func TestUnknownStableRingIDResolvesToNothing(t *testing.T) {
	sess := imageSpaceSession(t)
	RecordRecentImageStable(sess, []byte("PIC"), "received", ImageFromUser)
	if _, ok := ResolveRecentImage(sess, RecentImageRefPrefix+"r.deadbeef"); ok {
		t.Error("an unknown ring id must not resolve to some other picture")
	}
}

// The manifest is where the model learns which picture is which, so it must
// lead with the reference that stays true. Showing only the position taught it
// to carry a number that moves.
func TestManifestLeadsWithTheStableID(t *testing.T) {
	sess := imageSpaceSession(t)
	_, original := RecordRecentImageStable(sess, []byte("PHOTO"), "received from craig", ImageFromUser)
	RecordRecentImageStable(sess, []byte("RENDER"), "generated: a cat", ImageFromGenerated)

	m := RecentImageManifest(sess)
	if !strings.Contains(m, original) {
		t.Errorf("the stable id must appear in the manifest:\n%s", m)
	}
	// Position still shown, but as context rather than the handle.
	if !strings.Contains(m, "(now image#") {
		t.Errorf("the position should be a parenthetical:\n%s", m)
	}
	if !strings.Contains(m, "PREFER the image#r.") {
		t.Errorf("the manifest must say which form to carry:\n%s", m)
	}
	// The provenance split has to survive the relabelling.
	if !strings.Contains(m, "GIVEN") || !strings.Contains(m, "YOU MADE") {
		t.Errorf("provenance sections went missing:\n%s", m)
	}
}
