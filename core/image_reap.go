// Reclaiming the image store by age, alongside the counts that already bound it.
//
// The ring and the delivered-attachment store both prune on write, and both
// prune by COUNT: ten of an agent's own pictures, ten it was given, five hundred
// attachments per user. A count bounds a store that is still moving and does
// nothing at all to one that stopped. An agent that rendered ten pictures two
// years ago still holds all ten, because nothing has been written since to push
// them out, and a user who stopped talking keeps every attachment they were
// ever sent. That is the long tail this reclaims.
//
// The two halves do different jobs and neither replaces the other. The count
// runs on every write and is what keeps a burst from ever reaching the footprint
// an age window would only clean up later; the age window is what reaches a
// directory nobody has written to since.
//
// WHICH WAY THE ALLOWLIST POINTS IS THE SAFETY ARGUMENT, exactly as in
// workspace_reap.go. This removes only what it positively recognizes: a regular
// file, in a directory it navigated to by name, whose filename matches the shape
// the framework itself writes. Everything unrecognized survives. A missed shape
// means some bytes are not reclaimed and we notice because the disk did not
// shrink; the opposite policy loses somebody's picture silently.
//
// kept/ IS NEVER VISITED. Not skipped, not excluded, not filtered by a rule --
// the walk names recent/ and delivered/ and the store root, and never enumerates
// the store root's subdirectories, so there is no kept/ rule that a later edit
// could get wrong. Same structural argument pruneRecentImages makes: the library
// is a SIBLING of the ring, and unreachability is the guarantee. A kept image is
// the one thing in the store a user asked for by name, and it has no expiry.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The reap knobs. Each window is per class, and a window of zero means that
// class is never reaped -- so an operator can retire one half of this without
// touching the other, and "off" is expressible per class rather than only for
// the sweep as a whole.
const (
	TunableImageReap              = "tune_image_reap"
	TunableImageReapRingDays      = "tune_image_reap_ring_days"
	TunableImageReapDeliveredDays = "tune_image_reap_delivered_days"
	TunableImageReapOrphanHours   = "tune_image_reap_orphan_hours"
)

// imageReapInterval is how often the scheduled sweep does real work. The
// reconciler ticker runs every 30 minutes, which is the right cadence for
// checking that expected tasks exist and far too often for a filesystem scan
// that reclaims files measured in weeks. Held in memory rather than persisted:
// the only cost of a restart resetting it is one extra sweep at startup, and a
// sweep at startup is a good thing to have anyway.
const imageReapInterval = 6 * time.Hour

var (
	imageReapLast   time.Time
	imageReapLastMu sync.Mutex
)

// ImageReapClass says which of the store's subtrees a candidate came from. It
// exists for the report -- "300MB of orphaned renders" and "300MB of rings"
// call for different responses from whoever reads it.
type ImageReapClass string

const (
	// ImageReapRing is a per-user, per-agent recent-image entry.
	ImageReapRing ImageReapClass = "ring"
	// ImageReapDelivered is a stored chat attachment.
	ImageReapDelivered ImageReapClass = "delivered"
	// ImageReapOrphan is a bare render left at the store root by a generation
	// path that returned before its caller could remove it.
	ImageReapOrphan ImageReapClass = "orphan"
)

// ImageReapWindows is the retention window for each class. A zero (or negative)
// window disables that class entirely rather than reaping everything -- the
// direction that loses nothing when a knob is misread.
type ImageReapWindows struct {
	Ring      time.Duration
	Delivered time.Duration
	Orphan    time.Duration
}

// CurrentImageReapWindows reads the windows from the tunables registry, which
// is what both the scheduled sweep and the admin actions run against, so the
// dry run cannot be looking at a different policy than the reap.
func CurrentImageReapWindows() ImageReapWindows {
	return ImageReapWindows{
		Ring:      TuneDuration(TunableImageReapRingDays),
		Delivered: TuneDuration(TunableImageReapDeliveredDays),
		Orphan:    TuneDuration(TunableImageReapOrphanHours),
	}
}

// Any reports whether any class is eligible at all.
func (w ImageReapWindows) Any() bool {
	return w.Ring > 0 || w.Delivered > 0 || w.Orphan > 0
}

// ImageReapCandidate is one file eligible for removal.
type ImageReapCandidate struct {
	Class ImageReapClass
	Owner string // the store's directory name for the user, not the username
	Agent string // ring only; "" elsewhere
	Path  string // absolute
	Name  string
	Bytes int64 // the picture plus its sidecar, since both go together
	Age   time.Duration
}

// ringImageRE matches a ring entry: <unixnano>-<uuid>.png, the name
// recordRecentImage writes and nothing else in the tree does. The stamp is
// captured because it is a better clock than mtime -- it is the moment the
// picture entered the ring, it is what the ring already sorts by, and it
// survives a copy or a restore that would reset a modification time. Nineteen
// digits is UnixNano for every date from 2001 to 2262.
var ringImageRE = regexp.MustCompile(`^([0-9]{19})-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.png$`)

// orphanImageRE matches a bare-UUID render at the store root -- what the Gemini
// provider and writeImageTemp produce for a caller that is expected to remove it
// after reading. Every live consumer does remove it, synchronously, inside one
// tool call. So a file of this shape that is a day old is not a handoff in
// flight; it is one an error path returned past.
var orphanImageRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(png|jpg)$`)

// FindReapableImages returns what a reap WOULD remove. Read-only, and the same
// walk the reaper itself uses, so a dry run cannot disagree with the run.
//
// Owners come from the filesystem rather than from AuthListUsers. The store's
// directory names are safeRecentUser output, which is lossy, so a username does
// not round-trip back to a directory -- and going by the directories means a
// deleted user's leftovers are reclaimed too, which is otherwise a class of
// bytes nothing on the system can ever reach.
func FindReapableImages(w ImageReapWindows) []ImageReapCandidate {
	base := ImageDir()
	if base == "" {
		return nil
	}
	now := time.Now()
	var out []ImageReapCandidate

	if w.Ring > 0 {
		root := filepath.Join(base, "recent")
		for _, user := range imageStoreSubdirs(root) {
			userDir := filepath.Join(root, user)
			for _, agent := range imageStoreSubdirs(userDir) {
				out = append(out, scanRingImages(filepath.Join(userDir, agent), user, agent, now, w.Ring)...)
			}
		}
	}
	if w.Delivered > 0 {
		root := filepath.Join(base, "delivered")
		for _, user := range imageStoreSubdirs(root) {
			out = append(out, scanDeliveredImages(filepath.Join(root, user), user, now, w.Delivered)...)
		}
	}
	if w.Orphan > 0 {
		out = append(out, scanOrphanImages(base, now, w.Orphan)...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// imageStoreSubdirs lists one directory's immediate subdirectories. One level,
// never a walk: the depth of each subtree is known and fixed, so recursion would
// only buy the ability to reach somewhere this has no business being.
func imageStoreSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// scanRingImages reads ONE ring directory. Age comes from the filename stamp,
// not from mtime -- see ringImageRE.
func scanRingImages(dir, user, agent string, now time.Time, olderThan time.Duration) []ImageReapCandidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ImageReapCandidate
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		m := ringImageRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		nanos, perr := strconv.ParseInt(m[1], 10, 64)
		if perr != nil {
			continue
		}
		// A stamp in the future reads as a negative age and is therefore never
		// eligible, which is the right answer for a clock that moved.
		age := now.Sub(time.Unix(0, nanos))
		if age < olderThan {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			continue
		}
		size := fi.Size()
		// The sidecar leaves with the picture, so its bytes belong in the
		// figure the report quotes.
		if si, serr := os.Stat(ringSidecarPath(filepath.Join(dir, e.Name()))); serr == nil {
			size += si.Size()
		}
		out = append(out, ImageReapCandidate{
			Class: ImageReapRing, Owner: user, Agent: agent,
			Path: filepath.Join(dir, e.Name()), Name: e.Name(),
			Bytes: size, Age: age,
		})
	}
	return out
}

// scanDeliveredImages reads ONE user's attachment directory. Attachment names
// carry no timestamp, so mtime is the only clock there is.
func scanDeliveredImages(dir, user string, now time.Time, olderThan time.Duration) []ImageReapCandidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ImageReapCandidate
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if !deliveredImageExt(ext) {
			continue
		}
		// The same gate the serving path uses. A name that would not be served
		// as an attachment is not treated as one here either.
		if !ValidChatAttachmentID(strings.TrimSuffix(e.Name(), ext)) {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			continue
		}
		age := now.Sub(fi.ModTime())
		if age < olderThan {
			continue
		}
		out = append(out, ImageReapCandidate{
			Class: ImageReapDelivered, Owner: user,
			Path: filepath.Join(dir, e.Name()), Name: e.Name(),
			Bytes: fi.Size(), Age: age,
		})
	}
	return out
}

// scanOrphanImages reads the store ROOT, flat. Subdirectories are not entered,
// which is what keeps recent/, delivered/ and kept/ out of reach from here.
func scanOrphanImages(base string, now time.Time, olderThan time.Duration) []ImageReapCandidate {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []ImageReapCandidate
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		if !orphanImageRE.MatchString(e.Name()) {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			continue
		}
		age := now.Sub(fi.ModTime())
		if age < olderThan {
			continue
		}
		out = append(out, ImageReapCandidate{
			Class: ImageReapOrphan,
			Path:  filepath.Join(base, e.Name()), Name: e.Name(),
			Bytes: fi.Size(), Age: age,
		})
	}
	return out
}

// deliveredImageExt reports whether ext is one the attachment store writes.
// Read back off chatAttachmentExt rather than restated here, so a newly
// supported type becomes reapable the moment it becomes storable.
func deliveredImageExt(ext string) bool {
	for _, e := range chatAttachmentExt {
		if e == ext {
			return true
		}
	}
	return false
}

// ringSidecarPath is the metadata file beside a ring picture.
func ringSidecarPath(png string) string {
	return strings.TrimSuffix(png, ".png") + ".json"
}

// ReapImages removes what FindReapableImages reports and returns how many files
// went and how many bytes came back. A file that will not delete is logged and
// skipped: one unreadable entry is not a reason to abandon the rest, and the
// next sweep tries it again.
func ReapImages(w ImageReapWindows) (int, int64) {
	var files int
	var bytes int64
	for _, c := range FindReapableImages(w) {
		if err := os.Remove(c.Path); err != nil {
			Log("[image-reap] could not remove %s: %v", c.Path, err)
			continue
		}
		if c.Class == ImageReapRing {
			// Sidecar follows the picture, and a failure here costs bytes
			// rather than correctness: every reader lists .png files and reads
			// the .json beside one, so a sidecar with no picture is invisible.
			_ = os.Remove(ringSidecarPath(c.Path))
		}
		files++
		bytes += c.Bytes
	}
	// Nothing to notify. A ring ref resolves through the space, which reports a
	// missing entry as pruned rather than as an error; an attachment id that no
	// longer resolves already renders as a message without its picture; and an
	// orphan at the root is by definition a file no reference points at.
	return files, bytes
}

// FormatImageReapCandidates renders a dry run, largest first -- the order in
// which "is this worth doing" answers itself.
func FormatImageReapCandidates(list []ImageReapCandidate) string {
	if len(list) == 0 {
		return ""
	}
	var total int64
	byClass := map[ImageReapClass]int{}
	bytesByClass := map[ImageReapClass]int64{}
	for _, c := range list {
		total += c.Bytes
		byClass[c.Class]++
		bytesByClass[c.Class] += c.Bytes
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s in %d file(s) could be reclaimed:\n", HumanSize(total), len(list))
	for _, class := range []ImageReapClass{ImageReapRing, ImageReapDelivered, ImageReapOrphan} {
		if byClass[class] == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-12s %4d file(s)  %s\n", class, byClass[class], HumanSize(bytesByClass[class]))
	}
	b.WriteString("\nLargest:\n")
	for i, c := range list {
		if i >= 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(list)-10)
			break
		}
		where := c.Owner
		if where == "" {
			where = "(store root)"
		} else if c.Agent != "" {
			where += "/" + c.Agent
		}
		fmt.Fprintf(&b, "  %8s  %-42s %-22s %s old\n", HumanSize(c.Bytes), truncateMiddle(c.Name, 42), where, roundDuration(c.Age))
	}
	b.WriteString("\nOnly the recent-image rings, stored chat attachments, and bare renders left at the " +
		"store root are listed. Kept images are never read by this walk, so an image an agent was asked " +
		"to keep cannot appear here at any age.")
	return b.String()
}

func init() {
	RegisterTunable(TunableSpec{
		Key: TunableImageReap, Category: "Images", Label: "Reap old images automatically",
		Help: "Run the age-based image sweep on a schedule. Off leaves the store to the count-based " +
			"pruning that runs on every write, which bounds a busy store and never reclaims an idle " +
			"one. The admin actions below run either way; this only governs the scheduled sweep.",
		Kind: KindBool, Default: 1, Min: 0, Max: 1})
	RegisterTunable(TunableSpec{
		Key: TunableImageReapRingDays, Category: "Images", Label: "Recent-image retention",
		Help: "How long a picture stays in an agent's recent-image ring before age alone removes it. " +
			"The ring is already capped at ten of the agent's own pictures and ten it was given; this " +
			"is what reaches a ring nobody has written to since. 0 disables it. Kept images are never " +
			"affected at any value.",
		Kind: KindDays, Default: 30, Min: 0, Max: 3650})
	RegisterTunable(TunableSpec{
		Key: TunableImageReapDeliveredDays, Category: "Images", Label: "Chat attachment retention",
		Help: "How long a delivered chat attachment is stored before age alone removes it. Past this, " +
			"an old message renders without its picture, so keep it comfortably longer than anyone " +
			"scrolls back. 0 disables it, leaving only the 500-per-user cap.",
		Kind: KindDays, Default: 90, Min: 0, Max: 3650})
	RegisterTunable(TunableSpec{
		Key: TunableImageReapOrphanHours, Category: "Images", Label: "Orphaned render retention",
		Help: "How long a bare render left at the image-store root is kept. These are handoff files a " +
			"tool writes and removes within one call, so anything still here is one an error path " +
			"returned past. Low by design; raise it if you debug against those files. 0 disables it.",
		Kind: KindHours, Default: 24, Min: 0, Max: 8760})

	// Wired to the scheduler through the reconciler ticker, which is where the
	// framework already runs periodic upkeep (see managed_workspace_cleanup).
	// A persisted scheduled task would buy nothing here: there is no per-target
	// state to pre-arm and nothing to reconcile, so the work IS the reconcile.
	RegisterReconciler("image_reap", func(ctx context.Context) error {
		if !TuneBool(TunableImageReap) {
			return nil
		}
		w := CurrentImageReapWindows()
		if !w.Any() {
			return nil
		}
		if !dueForImageReap(time.Now()) {
			return nil
		}
		files, bytes := ReapImages(w)
		if files > 0 {
			Log("[image-reap] removed %d file(s), reclaimed %s", files, HumanSize(bytes))
		}
		return nil
	})

	RegisterMaintenanceFunc(
		"survey_reapable_images",
		"Survey reclaimable images (dry run)",
		"Read-only. Lists the recent-image ring entries, stored chat attachments, and orphaned "+
			"renders that are past their retention windows. Kept images are not read by this walk "+
			"and can never appear. Deletes nothing.",
		func(ctx context.Context) int {
			w := CurrentImageReapWindows()
			Log("[image-reap] dry run over %q — ring %s, attachments %s, orphans %s",
				ImageDir(), reapWindowLabel(w.Ring), reapWindowLabel(w.Delivered), reapWindowLabel(w.Orphan))
			if !w.Any() {
				Log("[image-reap] every window is disabled; nothing is eligible")
				return 0
			}
			list := FindReapableImages(w)
			if len(list) == 0 {
				Log("[image-reap] nothing eligible")
				return 0
			}
			Log("[image-reap]\n%s", FormatImageReapCandidates(list))
			return len(list)
		},
	)
	RegisterMaintenanceFunc(
		"reap_images",
		"Reclaim old images (DELETES)",
		"Removes exactly what the dry run above lists. Run the dry run first — it uses the same "+
			"walk, so what it shows is what this removes.",
		func(ctx context.Context) int {
			w := CurrentImageReapWindows()
			if !w.Any() {
				Log("[image-reap] every window is disabled; nothing removed")
				return 0
			}
			list := FindReapableImages(w)
			if len(list) == 0 {
				Log("[image-reap] nothing eligible; nothing removed")
				return 0
			}
			Log("[image-reap] removing %d file(s):\n%s", len(list), FormatImageReapCandidates(list))
			files, bytes := ReapImages(w)
			Log("[image-reap] removed %d file(s), reclaimed %s", files, HumanSize(bytes))
			return files
		},
	)
}

// dueForImageReap rate-limits the scheduled sweep to imageReapInterval, and
// records the attempt as it answers so two reconciler passes cannot both run.
func dueForImageReap(now time.Time) bool {
	imageReapLastMu.Lock()
	defer imageReapLastMu.Unlock()
	if !imageReapLast.IsZero() && now.Sub(imageReapLast) < imageReapInterval {
		return false
	}
	imageReapLast = now
	return true
}

// reapWindowLabel renders a window for the log, naming the disabled case rather
// than printing "0s" and leaving the reader to work out what that means.
func reapWindowLabel(d time.Duration) string {
	if d <= 0 {
		return "disabled"
	}
	return roundDuration(d)
}
