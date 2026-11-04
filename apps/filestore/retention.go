// Reclaiming a file store, by recognizing what may go rather than what
// may stay.
//
// WHICH WAY THE ALLOWLIST POINTS IS THE WHOLE SAFETY ARGUMENT, and it is
// borrowed wholesale from core/workspace_reap.go. This removes only what
// it positively recognizes: a DIRECTORY that is a direct child of a
// store root, in a store whose admin set a retention window, whose
// newest content is older than that window. Everything else is
// untouchable — the root itself, loose files at the root, anything
// nested, any store with no window set.
//
// The alternative (delete everything except registered exclusions) fails
// in the wrong direction. A missed exclusion there destroys the only
// copy of somebody's evidence, silently, and surfaces when they go
// looking for it months later. A missed case here means some disk is not
// reclaimed, and we notice because the disk did not shrink.
//
// NOTHING DELETES ON A TIMER. The window makes a folder eligible; an
// admin runs the sweep from Maintenance, and the dry run there uses this
// same function, so what it lists is exactly what the delete removes.
// Evidence is not the kind of thing to reclaim unattended at 3am — and a
// bundle is often the only artifact of an incident that still exists.
//
// The one exception is a store an admin marked TEMPORARY (Store.Ephemeral):
// a landing zone whose contents are copied onward, which has said it is not
// evidence. Its folders go on a timer once they outlive its lifetime
// (sweepTemporaryStores), and even then never while something is using them.

package filestore

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// ExpiredBundle is one folder eligible for removal.
type ExpiredBundle struct {
	Store  string // store name, for the report
	Slug   string
	Folder string    // the subfolder's name, as the tools would take it
	Path   string    // resolved absolute path
	Newest time.Time // newest content in it — what its age is measured from
	Bytes  int64
}

// Age is how long since anything in the folder last changed.
func (b ExpiredBundle) Age() time.Duration { return time.Since(b.Newest) }

// bundleNewest reports the most recent modification time in a folder,
// measured from the directory itself and its DIRECT children.
//
// Not a full walk, deliberately: a bundle expands to tens of thousands
// of files and this runs across every folder of every store. The cost of
// the shallow read is that a write deep inside a folder may not refresh
// the figure — so the window wants to be generous, and the help text
// says so rather than leaving it as a surprise.
func bundleNewest(dir string) (time.Time, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, err
	}
	newest := fi.ModTime()
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable is NOT eligible: refusing to delete what we cannot
		// inspect is the right direction to fail.
		return time.Time{}, err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, nil
}

// folderBytes sums a folder's size. Best-effort: an unreadable subtree
// contributes what was readable rather than failing the report.
func folderBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// FindExpiredBundles lists every folder eligible for removal, oldest
// first. Empty when no store has a retention window — which is the
// default, and the state every store starts in. A stopped ctx ends the walk
// at the next folder, with what it found so far.
func FindExpiredBundles(ctx context.Context, db Database) []ExpiredBundle {
	return findExpired(ctx, db, false).bundles
}

// expiredScan is what a sweep found: the folders it may remove, and the ones
// it is leaving alone because this server's user could not remove them.
type expiredScan struct {
	bundles []ExpiredBundle
	// blocked is one line per store or folder left alone, saying why. Not
	// attempted, so never half-deleted, and said rather than skipped quietly:
	// a cleanup somebody set up that can never happen is a setting to fix.
	blocked []string
}

// blockedNote is the scan's left-alone list as one clause, "" when empty.
func (sc expiredScan) blockedNote() string {
	if len(sc.blocked) == 0 {
		return ""
	}
	more := ""
	if len(sc.blocked) > 1 {
		more = fmt.Sprintf(" (and %d more)", len(sc.blocked)-1)
	}
	return fmt.Sprintf("left alone, because this server cannot delete there: %s%s", sc.blocked[0], more)
}

// findExpired is FindExpiredBundles, limited to temporary stores when
// temporaryOnly is set: what the automatic sweep may take without an admin.
//
// A temporary store's window is its lifetime in hours; any other store's is
// its retention in days. A folder something is using (pathhold) is never
// listed, however old: an extraction reading or writing deep inside one does
// not refresh the age read here.
//
// Nor is a folder this server's user could not remove whole (removeBlocker):
// it goes on the blocked list instead, and a store whose root it cannot delete
// in at all is listed there once and not read. What saved a temporary store
// pointed at /var/log was the operating system refusing, every fifteen
// minutes for a week; a folder the user could PARTLY delete would have been
// emptied and then reported as failing.
func findExpired(ctx context.Context, db Database, temporaryOnly bool) expiredScan {
	var sc expiredScan
	if db == nil {
		return sc
	}
	for _, st := range ListStores(db) {
		var cutoff time.Time
		switch {
		case st.Ephemeral:
			cutoff = time.Now().Add(-st.lifetime())
		case temporaryOnly:
			continue
		case st.RetentionDays > 0:
			cutoff = time.Now().AddDate(0, 0, -st.RetentionDays)
		default:
			continue // no window set: nothing in this store is ever eligible
		}
		if why := cleanupBlocker(st.Path); why != "" {
			sc.blocked = append(sc.blocked, st.Name+": "+why)
			continue
		}
		entries, err := os.ReadDir(st.Path)
		if err != nil {
			Log("[filestore.retention] %s: cannot read %s (%v), skipped", st.Name, st.Path, err)
			continue
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return sc
			}
			if !e.IsDir() {
				continue // loose files at the root are never touched
			}
			// SubRoot resolves symlinks and refuses anything not strictly
			// below the root. A symlinked folder pointing elsewhere on the
			// disk must not become a delete target because it happened to
			// be listed here.
			dir, err := SubRoot(st.Path, e.Name())
			if err != nil {
				Log("[filestore.retention] %s/%s did not resolve inside the store (%v): skipped", st.Name, e.Name(), err)
				continue
			}
			newest, err := bundleNewest(dir)
			if err != nil {
				continue // unreadable: not eligible
			}
			if !newest.Before(cutoff) {
				continue
			}
			if pathhold.InUse(dir) {
				Debug("[filestore.retention] %s/%s is past its window but in use: kept", st.Name, e.Name())
				continue
			}
			if why := removeBlocker(dir); why != "" {
				sc.blocked = append(sc.blocked, st.Name+" / "+e.Name()+": "+why)
				continue
			}
			sc.bundles = append(sc.bundles, ExpiredBundle{
				Store: st.Name, Slug: st.Slug, Folder: e.Name(),
				Path: dir, Newest: newest, Bytes: folderBytes(dir),
			})
		}
	}
	sort.Slice(sc.bundles, func(i, j int) bool { return sc.bundles[i].Newest.Before(sc.bundles[j].Newest) })
	return sc
}

// FormatExpiredBundles renders candidates for the maintenance log — the
// same text for the dry run and the delete, so what an admin approved is
// what they read.
func FormatExpiredBundles(list []ExpiredBundle) string {
	if len(list) == 0 {
		return "(nothing eligible)"
	}
	var b strings.Builder
	var total int64
	for _, c := range list {
		total += c.Bytes
		fmt.Fprintf(&b, "  %s / %s: %s, last touched %s ago\n",
			c.Store, c.Folder, HumanSize(c.Bytes), c.Age().Round(time.Hour))
	}
	fmt.Fprintf(&b, "  %d folder(s), %s total", len(list), HumanSize(total))
	return b.String()
}

// ReapExpiredBundles removes exactly what FindExpiredBundles reports.
// Returns how many folders went and how many bytes that reclaimed. A stopped
// ctx removes nothing when it stops the walk, and otherwise ends the removing
// at the next folder: each folder goes on its own.
func ReapExpiredBundles(ctx context.Context, db Database) (int, int64) {
	return reapExpired(ctx, db, false)
}

// reapExpired removes what findExpired reports.
func reapExpired(ctx context.Context, db Database, temporaryOnly bool) (int, int64) {
	sc := findExpired(ctx, db, temporaryOnly)
	list := sc.bundles
	if ctx.Err() != nil {
		return 0, 0
	}
	var gone, failed int
	var freed int64
	var firstFailure string
	for _, c := range list {
		if ctx.Err() != nil {
			break
		}
		// Checked again at the moment of removal: work may have taken the
		// folder up since the list was made.
		if pathhold.InUse(c.Path) {
			continue
		}
		if err := os.RemoveAll(c.Path); err != nil {
			Log("[filestore.retention] could not remove %s: %v", c.Path, err)
			if failed == 0 {
				firstFailure = fmt.Sprintf("%s / %s: %v", c.Store, c.Folder, err)
			}
			failed++
			continue
		}
		gone++
		freed += c.Bytes
		// One line per deletion, naming the store and the folder. A
		// bundle that somebody comes looking for later deserves a record
		// of what happened to it more than the log deserves brevity.
		Log("[filestore.retention] removed %s / %s (%s, last touched %s ago)",
			c.Store, c.Folder, HumanSize(c.Bytes), c.Age().Round(time.Hour))
	}
	// A folder that would not go is still taking the space it was meant to
	// give back, so the run that met it failed, whatever else it removed.
	// No-op outside a maintenance run.
	//
	// What was left alone for want of permission is a failure too, though
	// nothing was tried: the cleanup somebody set up is not happening, and it
	// stays on System Status until the store or its permissions are fixed.
	var problems []string
	if failed > 0 {
		problems = append(problems, fmt.Sprintf("could not remove %d, first %s", failed, firstFailure))
	}
	if note := sc.blockedNote(); note != "" {
		problems = append(problems, note)
	}
	if len(problems) > 0 {
		ReportMaintenanceFailure(ctx, fmt.Sprintf("removed %d folder(s), %s reclaimed; %s", gone, HumanSize(freed), strings.Join(problems, "; ")))
	}
	return gone, freed
}

// temporarySweepEvery is how often temporary stores are swept. Their lifetimes
// are in hours, so a folder outlives its lifetime by at most this much.
const temporarySweepEvery = 15 * time.Minute

// temporarySweepKey is the temporary-store sweep as a maintenance pass. The
// timer runs it through the maintenance runner, so each sweep is recorded like
// a pressed one, and a folder that would not go is on System Status.
const temporarySweepKey = "sweep_temporary_stores"

// sweepTemporaryStores removes, on a timer, the folders of temporary stores
// that have outlived their lifetime. Only temporary stores: everything else
// waits for an admin, as the note at the top of this file says.
func (T *FileStoreApp) sweepTemporaryStores() {
	tick := time.NewTicker(temporarySweepEvery)
	defer tick.Stop()
	for range tick.C {
		RunMaintenanceFunc(context.Background(), temporarySweepKey)
	}
}

// sweepTemporaryPass is one sweep: the expired folders of temporary stores,
// removed, and how many.
func sweepTemporaryPass(ctx context.Context, db Database) int {
	n, freed := reapExpired(ctx, db, true)
	if n > 0 {
		Log("[filestore.retention] temporary stores: removed %d expired folder(s), %s", n, HumanSize(freed))
	}
	if ctx.Err() != nil {
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("stopped after removing %d folder(s), %s reclaimed", n, HumanSize(freed)))
	} else {
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("removed %d expired folder(s), %s reclaimed", n, HumanSize(freed)))
	}
	return n
}

// registerRetentionMaintenance wires the dry run and the delete as a
// pair, matching the workspace reaper: same walk behind both, so the
// list an admin reads is the list that gets removed.
func registerRetentionMaintenance(app *FileStoreApp) {
	// The temporary-store sweep is not one of the pair: it takes only what a
	// temporary store's own lifetime already says may go, and it runs on its
	// own. Here so it can also be run now, and so each run is recorded.
	RegisterMaintenanceFunc("Housekeeping", temporarySweepKey,
		"Remove expired temporary store folders",
		"Removes the folders of temporary stores that have outlived their lifetime. Runs on its own "+
			"every 15 minutes; this runs it now. Stores that are not temporary are never touched by it.",
		func(ctx context.Context) int { return sweepTemporaryPass(ctx, app.DB) })
	RegisterMaintenanceFunc("Reclaim space",
		"list_expired_bundles",
		"List expired file-store folders (dry run)",
		"Shows which folders are past their store's retention window. Deletes nothing. "+
			"A store with no window set never appears here, that is the default, and it means "+
			"nothing in it is ever eligible.",
		func(ctx context.Context) int {
			sc := findExpired(ctx, app.DB, false)
			list := sc.bundles
			if ctx.Err() != nil {
				ReportMaintenanceOutcome(ctx, fmt.Sprintf("stopped part way, %d folder(s) found by then", len(list)))
				return len(list)
			}
			// The dry run says what the delete would leave alone, and why, so
			// it is read before anybody presses delete.
			note := sc.blockedNote()
			if len(list) == 0 {
				Log("[filestore.retention] nothing eligible")
				if note != "" {
					ReportMaintenanceOutcome(ctx, "nothing eligible; "+note)
				}
				return 0
			}
			Log("[filestore.retention] eligible:\n%s", FormatExpiredBundles(list))
			if note != "" {
				ReportMaintenanceOutcome(ctx, fmt.Sprintf("%d folder(s) eligible; %s", len(list), note))
			}
			return len(list)
		},
	)
	RegisterMaintenanceFunc("Reclaim space",
		"reap_expired_bundles",
		"Delete expired file-store folders (DELETES)",
		"Removes exactly what the dry run above lists: folders directly under a store root whose "+
			"newest content is past that store's retention window. Run the dry run first: it uses "+
			"the same walk, so what it shows is what this removes. Not recoverable.",
		func(ctx context.Context) int {
			sc := findExpired(ctx, app.DB, false)
			list := sc.bundles
			if ctx.Err() != nil {
				ReportMaintenanceOutcome(ctx, "stopped before removing anything")
				return 0
			}
			if len(list) == 0 {
				Log("[filestore.retention] nothing eligible; nothing removed")
				// Nothing to remove can still be a cleanup that cannot
				// happen: what was left alone is said, not swallowed.
				if note := sc.blockedNote(); note != "" {
					ReportMaintenanceFailure(ctx, "nothing removed; "+note)
				}
				return 0
			}
			Log("[filestore.retention] removing %d folder(s):\n%s", len(list), FormatExpiredBundles(list))
			gone, freed := ReapExpiredBundles(ctx, app.DB)
			Log("[filestore.retention] removed %d folder(s), reclaimed %s", gone, HumanSize(freed))
			if ctx.Err() != nil {
				ReportMaintenanceOutcome(ctx, fmt.Sprintf("stopped after removing %d folder(s), %s reclaimed", gone, HumanSize(freed)))
			}
			return gone
		},
	)
}
