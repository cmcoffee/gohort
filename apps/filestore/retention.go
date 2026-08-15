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
// default, and the state every store starts in.
func FindExpiredBundles(db Database) []ExpiredBundle {
	if db == nil {
		return nil
	}
	var out []ExpiredBundle
	for _, st := range ListStores(db) {
		if st.RetentionDays <= 0 {
			continue // no window set: nothing in this store is ever eligible
		}
		cutoff := time.Now().AddDate(0, 0, -st.RetentionDays)
		entries, err := os.ReadDir(st.Path)
		if err != nil {
			Log("[filestore.retention] %s: cannot read %s (%v) — skipped", st.Name, st.Path, err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue // loose files at the root are never touched
			}
			// SubRoot resolves symlinks and refuses anything not strictly
			// below the root. A symlinked folder pointing elsewhere on the
			// disk must not become a delete target because it happened to
			// be listed here.
			dir, err := SubRoot(st.Path, e.Name())
			if err != nil {
				Log("[filestore.retention] %s/%s did not resolve inside the store (%v) — skipped", st.Name, e.Name(), err)
				continue
			}
			newest, err := bundleNewest(dir)
			if err != nil {
				continue // unreadable: not eligible
			}
			if !newest.Before(cutoff) {
				continue
			}
			out = append(out, ExpiredBundle{
				Store: st.Name, Slug: st.Slug, Folder: e.Name(),
				Path: dir, Newest: newest, Bytes: folderBytes(dir),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Newest.Before(out[j].Newest) })
	return out
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
		fmt.Fprintf(&b, "  %s / %s — %s, last touched %s ago\n",
			c.Store, c.Folder, HumanSize(c.Bytes), c.Age().Round(time.Hour))
	}
	fmt.Fprintf(&b, "  %d folder(s), %s total", len(list), HumanSize(total))
	return b.String()
}

// ReapExpiredBundles removes exactly what FindExpiredBundles reports.
// Returns how many folders went and how many bytes that reclaimed.
func ReapExpiredBundles(db Database) (int, int64) {
	list := FindExpiredBundles(db)
	var gone int
	var freed int64
	for _, c := range list {
		if err := os.RemoveAll(c.Path); err != nil {
			Log("[filestore.retention] could not remove %s: %v", c.Path, err)
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
	return gone, freed
}

// registerRetentionMaintenance wires the dry run and the delete as a
// pair, matching the workspace reaper: same walk behind both, so the
// list an admin reads is the list that gets removed.
func registerRetentionMaintenance(app *FileStoreApp) {
	RegisterMaintenanceFunc(
		"list_expired_bundles",
		"List expired file-store folders (dry run)",
		"Shows which bundle folders are past their store's retention window. Deletes nothing. "+
			"A store with no window set never appears here — that is the default, and it means "+
			"nothing in it is ever eligible.",
		func(ctx context.Context) int {
			list := FindExpiredBundles(app.DB)
			if len(list) == 0 {
				Log("[filestore.retention] nothing eligible")
				return 0
			}
			Log("[filestore.retention] eligible:\n%s", FormatExpiredBundles(list))
			return len(list)
		},
	)
	RegisterMaintenanceFunc(
		"reap_expired_bundles",
		"Delete expired file-store folders (DELETES)",
		"Removes exactly what the dry run above lists: folders directly under a store root whose "+
			"newest content is past that store's retention window. Run the dry run first — it uses "+
			"the same walk, so what it shows is what this removes. Not recoverable.",
		func(ctx context.Context) int {
			list := FindExpiredBundles(app.DB)
			if len(list) == 0 {
				Log("[filestore.retention] nothing eligible; nothing removed")
				return 0
			}
			Log("[filestore.retention] removing %d folder(s):\n%s", len(list), FormatExpiredBundles(list))
			gone, freed := ReapExpiredBundles(app.DB)
			Log("[filestore.retention] removed %d folder(s), reclaimed %s", gone, HumanSize(freed))
			return gone
		},
	)
}
