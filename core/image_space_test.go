package core

// The image space and the reference forms that resolve through it. The point of
// the space is that the MODEL no longer does filesystem hygiene: it never
// deletes, and an image it made three turns ago is still addressable.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if ref := RecordRecentImage(sess, data, "generated: a cat"); ref != "image#1" {
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
	RecordRecentImage(sess, first, "first")
	RecordRecentImage(sess, second, "second")

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
		RecordRecentImage(sess, testPNG(t, 8+i, 8), "img")
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
	RecordRecentImage(alice, testPNG(t, 8, 8), "alice's")
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
	if ref := RecordRecentImage(anon, testPNG(t, 8, 8), "x"); ref != "" {
		t.Errorf("ref = %q, want empty for a session with no user", ref)
	}
	if RecentImages(anon) != nil {
		t.Error("an anonymous session has no space")
	}
	if RecentImageManifest(anon) != "" {
		t.Error("an empty space renders no manifest")
	}
	if ref := RecordRecentImage(nil, testPNG(t, 8, 8), "x"); ref != "" {
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
	RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a cat")
	RecordRecentImage(sess, testPNG(t, 9, 9), "edited image#1: snowy")

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
	RecordRecentImage(sess, data, "one")

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
	RecordRecentImage(sess, first, "first")
	RecordRecentImage(sess, second, "second")

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
