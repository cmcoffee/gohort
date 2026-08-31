package core

// The age reaper. The property worth defending hardest is the one that isn't
// about reclaiming anything: a kept image has no expiry, and the walk must be
// unable to reach one rather than merely declining to.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Sizes for the fixtures, so a reclaimed-bytes figure can be asserted exactly.
const (
	fakeImageBytes   = 1024
	fakeSidecarBytes = 128
)

// ringName builds a ring filename whose stamp is the given age in the past.
// That stamp, not mtime, is how scanRingImages dates an entry — which is why
// every ring fixture below is written with a mtime of now.
func ringName(age time.Duration) string {
	return strconv.FormatInt(time.Now().Add(-age).UnixNano(), 10) + "-0123abcd-4567-89ef-0123-456789abcdef.png"
}

// imageReapStore points the store at a temp dir for one test.
func imageReapStore(t *testing.T) string {
	t.Helper()
	saved := imageDir
	dir := t.TempDir()
	SetImageDir(dir)
	t.Cleanup(func() { imageDir = saved })
	return dir
}

func reapFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// allImageReapWindows is aggressive on every class, so a test that expects
// nothing to be reaped is saying the walk cannot reach it rather than that a
// window happened to spare it.
var allImageReapWindows = ImageReapWindows{Ring: 24 * time.Hour, Delivered: 24 * time.Hour, Orphan: time.Hour}

// THE invariant. A kept image is the one thing in the store a user asked for by
// name; no window may ever reach it, however old it is.
//
// Deliberately named to defeat the weaker reading of that guarantee. A test
// using ordinary kept names would pass on name shape alone and keep passing if
// someone replaced the named walk with a generic one over the store root. These
// fixtures carry EXACTLY the names the reaper recognizes elsewhere — an aged
// ring entry, its sidecar, an attachment, a bare render — so the only thing
// standing between them and deletion is that kept/ is never enumerated.
func TestReapNeverTouchesKeptImages(t *testing.T) {
	base := imageReapStore(t)
	lib := filepath.Join(base, "kept", "alice", "researcher")
	ancient := 5 * 365 * 24 * time.Hour
	decoys := []string{
		"brand_mark.png",
		"brand_mark.json",
		ringName(ancient),
		ringSidecarPath(ringName(ancient)),
		"att_0123abcd-4567-89ef-0123-456789abcdef.png",
		"0123abcd-4567-89ef-0123-456789abcdef.png",
	}
	for _, name := range decoys {
		writeAged(t, filepath.Join(lib, name), fakeImageBytes, ancient)
	}

	if list := FindReapableImages(allImageReapWindows); len(list) != 0 {
		t.Fatalf("kept images must be unreachable, got %d candidate(s): %+v", len(list), list)
	}
	if files, _ := ReapImages(allImageReapWindows); files != 0 {
		t.Fatalf("reaped %d file(s) from a store holding only kept images", files)
	}
	for _, name := range decoys {
		if !reapFileExists(filepath.Join(lib, name)) {
			t.Errorf("%s was removed from the library; a kept image has no expiry", name)
		}
	}
}

func TestReapDropsOldRingEntriesWithSidecars(t *testing.T) {
	base := imageReapStore(t)
	dir := filepath.Join(base, "recent", "alice", "researcher")
	old := ringName(40 * 24 * time.Hour)
	fresh := ringName(2 * time.Hour)
	for _, name := range []string{old, fresh} {
		writeAged(t, filepath.Join(dir, name), fakeImageBytes, 0)
		writeAged(t, filepath.Join(dir, ringSidecarPath(name)), fakeSidecarBytes, 0)
	}

	files, bytes := ReapImages(ImageReapWindows{Ring: 30 * 24 * time.Hour})
	if files != 1 {
		t.Fatalf("reaped %d file(s), want exactly the one past the window", files)
	}
	if bytes != fakeImageBytes+fakeSidecarBytes {
		t.Errorf("reclaimed %d bytes, want %d — the sidecar's bytes count too", bytes, fakeImageBytes+fakeSidecarBytes)
	}
	if reapFileExists(filepath.Join(dir, old)) {
		t.Error("the aged ring entry survived")
	}
	if reapFileExists(filepath.Join(dir, ringSidecarPath(old))) {
		t.Error("the sidecar must leave with its picture")
	}
	if !reapFileExists(filepath.Join(dir, fresh)) || !reapFileExists(filepath.Join(dir, ringSidecarPath(fresh))) {
		t.Error("an entry inside the window was removed")
	}
}

// The allowlist direction: anything whose name the framework did not write is
// not a candidate, however old and whatever directory it turned up in.
func TestReapIgnoresUnrecognizedNames(t *testing.T) {
	base := imageReapStore(t)
	ring := filepath.Join(base, "recent", "alice", "researcher")
	old := 400 * 24 * time.Hour
	strangers := []string{
		filepath.Join(ring, "my-holiday-photo.png"),
		filepath.Join(ring, "notes.txt"),
		filepath.Join(ring, "1234567890-not-a-uuid.png"),
		filepath.Join(base, "delivered", "alice", "photo.png"),
		filepath.Join(base, "delivered", "alice", "att_nope.txt"),
		filepath.Join(base, "gen-cafe.png"),
		filepath.Join(base, "0123abcd-4567-89ef-0123-456789abcdef.gif"),
	}
	for _, p := range strangers {
		writeAged(t, p, fakeImageBytes, old)
	}

	if list := FindReapableImages(allImageReapWindows); len(list) != 0 {
		t.Fatalf("unrecognized names must never be candidates, got %+v", list)
	}
	for _, p := range strangers {
		if !reapFileExists(p) {
			t.Errorf("%s was removed; only names the framework writes are eligible", filepath.Base(p))
		}
	}
}

func TestReapDeliveredAttachmentsByAge(t *testing.T) {
	base := imageReapStore(t)
	dir := filepath.Join(base, "delivered", "alice")
	old := "att_0123abcd-4567-89ef-0123-456789abcdef.png"
	fresh := "att_fedcba98-7654-3210-fedc-ba9876543210.jpg"
	writeAged(t, filepath.Join(dir, old), fakeImageBytes, 120*24*time.Hour)
	writeAged(t, filepath.Join(dir, fresh), fakeImageBytes, 3*24*time.Hour)

	if files, _ := ReapImages(ImageReapWindows{Delivered: 90 * 24 * time.Hour}); files != 1 {
		t.Fatalf("reaped %d file(s), want 1", files)
	}
	if reapFileExists(filepath.Join(dir, old)) {
		t.Error("an attachment past the window survived")
	}
	if !reapFileExists(filepath.Join(dir, fresh)) {
		t.Error("an attachment inside the window was removed")
	}
}

// Orphans are read from the store root, flat. The subtrees hanging off it must
// be invisible to that scan even when their contents would otherwise match.
func TestReapOrphansReadTheRootOnly(t *testing.T) {
	base := imageReapStore(t)
	orphan := "0123abcd-4567-89ef-0123-456789abcdef.png"
	writeAged(t, filepath.Join(base, orphan), fakeImageBytes, 48*time.Hour)
	writeAged(t, filepath.Join(base, "fedcba98-7654-3210-fedc-ba9876543210.jpg"), fakeImageBytes, time.Minute)
	// Same shape, one level down, inside the library.
	nested := filepath.Join(base, "kept", "alice", "agent", orphan)
	writeAged(t, nested, fakeImageBytes, 48*time.Hour)

	w := ImageReapWindows{Orphan: 24 * time.Hour}
	list := FindReapableImages(w)
	if len(list) != 1 || list[0].Name != orphan || list[0].Class != ImageReapOrphan {
		t.Fatalf("candidates = %+v, want only the aged render at the root", list)
	}
	ReapImages(w)
	if reapFileExists(filepath.Join(base, orphan)) {
		t.Error("the aged orphan survived")
	}
	if !reapFileExists(nested) {
		t.Error("the root scan reached into a subdirectory")
	}
}

// A zero window is "never", not "everything" — the reading that loses nothing
// when a knob is misread.
func TestZeroWindowDisablesItsClass(t *testing.T) {
	base := imageReapStore(t)
	ring := filepath.Join(base, "recent", "alice", "researcher", ringName(400*24*time.Hour))
	att := filepath.Join(base, "delivered", "alice", "att_0123abcd-4567-89ef-0123-456789abcdef.png")
	orphan := filepath.Join(base, "0123abcd-4567-89ef-0123-456789abcdef.png")
	writeAged(t, ring, fakeImageBytes, 0)
	writeAged(t, att, fakeImageBytes, 400*24*time.Hour)
	writeAged(t, orphan, fakeImageBytes, 400*24*time.Hour)

	var none ImageReapWindows
	if none.Any() {
		t.Fatal("an all-zero window set must report nothing eligible")
	}
	if files, _ := ReapImages(none); files != 0 {
		t.Fatalf("reaped %d file(s) with every window disabled", files)
	}

	// One class on, the other two off: only that class moves.
	if files, _ := ReapImages(ImageReapWindows{Delivered: 30 * 24 * time.Hour}); files != 1 {
		t.Fatalf("reaped %d file(s), want only the attachment", files)
	}
	if !reapFileExists(ring) || !reapFileExists(orphan) {
		t.Error("a disabled class was reaped anyway")
	}
}

// The scheduled sweep must not rescan on every reconciler tick.
func TestScheduledReapIsRateLimited(t *testing.T) {
	saved := imageReapLast
	t.Cleanup(func() { imageReapLast = saved })
	imageReapLast = time.Time{}

	now := time.Now()
	if !dueForImageReap(now) {
		t.Fatal("the first sweep after startup must run")
	}
	if dueForImageReap(now.Add(imageReapInterval - time.Minute)) {
		t.Error("a second sweep inside the interval must be skipped")
	}
	if !dueForImageReap(now.Add(imageReapInterval + time.Minute)) {
		t.Error("a sweep past the interval must run")
	}
}

// The windows the sweep runs against are the ones the admin actions report, so
// a dry run cannot describe a different policy than the reap applies.
func TestDefaultWindowsAreLiveAndOrdered(t *testing.T) {
	w := CurrentImageReapWindows()
	if !w.Any() {
		t.Fatal("the shipped defaults must leave the sweep able to reclaim something")
	}
	// A handoff file is transient by construction, so it must expire strictly
	// sooner than anything a conversation can still refer to. Ring against
	// attachments is a policy call rather than a law -- an attachment is
	// somebody's photo and a ring entry is an agent's working set, so an
	// attachment may outlive one, but it should never expire FIRST.
	if w.Orphan >= w.Ring {
		t.Errorf("windows = %+v; a handoff file must expire before a ring entry", w)
	}
	if w.Delivered < w.Ring {
		t.Errorf("windows = %+v; an attachment someone may scroll back to must not expire before the agent's own working set", w)
	}
	if reapWindowLabel(0) != "disabled" {
		t.Error("a zero window must read as disabled rather than as 0s")
	}
}
