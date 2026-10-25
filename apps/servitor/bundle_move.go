package servitor

// Moving an evidence bundle into a file store (docs/design/servitor-systems-model.md,
// stage 3).
//
// Evidence lives in file store folders now: a store with "Index folders as
// evidence" on indexes each folder the way a bundle is indexed, and an agent
// given the store gets summarize and timeline tools for each one. This copies a
// bundle's files into a new folder of such a store, which then indexes it. The
// bundle is left as it was, until its owner deletes it.
//
// What is copied is what the bundle kept, not what was uploaded: the upload was
// deleted once ingested, and only text files' lines were stored. So a binary,
// or an archive the ingest could not open, has nothing to copy and is named in
// the outcome rather than left out quietly.
//
// A bundle can be gigabytes, so the move runs in the background, the page
// polls it (an ActionList row per store: spinner, how far, Stop), a page
// arriving mid-move rejoins it, and its outcome is kept a while.
//
//	GET  /api/bundle/move-targets?appliance_id=   the stores it can go to
//	POST /api/bundle/move?appliance_id=&store=    start it
//	GET  /api/bundle/move?appliance_id=&store=    {"progress"} | {"outcome"}
//	POST /api/bundle/move?appliance_id=&cancel=1  stop it

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// bundleMoveKept is how long a finished move's outcome stays to read.
const bundleMoveKept = 30 * time.Minute

// bundleMove is one bundle being copied into a store.
type bundleMove struct {
	store, storeName string
	total            int
	totalBytes       int64
	started          time.Time

	mu      sync.Mutex
	files   int   // files done so far, copied or not
	bytes   int64 // bytes written so far
	ended   time.Time
	outcome string
	cancel  context.CancelFunc
}

func (m *bundleMove) progress() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("Copying %d of %d files, %s of %s - %ds", m.files, m.total,
		HumanSize(m.bytes), HumanSize(m.totalBytes), int(time.Since(m.started).Seconds()))
}

// status is what the page polls: progress while it runs, the outcome after.
func (m *bundleMove) status() map[string]any {
	m.mu.Lock()
	ended, outcome := m.ended, m.outcome
	m.mu.Unlock()
	if ended.IsZero() {
		return map[string]any{"progress": m.progress()}
	}
	return map[string]any{"outcome": outcome}
}

// bundleMoves is the moves this server is running, one per bundle, and each
// one's outcome for a while after.
var bundleMoves = struct {
	sync.Mutex
	at map[string]*bundleMove
}{at: map[string]*bundleMove{}}

func bundleMoveKey(owner, id string) string { return owner + "/" + id }

// currentBundleMove is the bundle's move, running or recently ended.
func currentBundleMove(key string) (*bundleMove, bool) {
	bundleMoves.Lock()
	defer bundleMoves.Unlock()
	m, ok := bundleMoves.at[key]
	if !ok {
		return nil, false
	}
	m.mu.Lock()
	stale := !m.ended.IsZero() && time.Since(m.ended) > bundleMoveKept
	m.mu.Unlock()
	if stale {
		delete(bundleMoves.at, key)
		return nil, false
	}
	return m, true
}

// evidenceDrop is the file-store side, when one is compiled in.
func evidenceDrop() (appstores.EvidenceDrop, bool) {
	d, ok := appstores.Provider.(appstores.EvidenceDrop)
	return d, ok && d != nil
}

// movableBundle resolves the request's bundle for someone who may move it.
func (T *Servitor) movableBundle(c *jsonapi.Call) (rec Appliance, owner string, err error) {
	rec, owner, _, found := T.resolveAppliance(c.User, c.UDB, strings.TrimSpace(c.R.URL.Query().Get("appliance_id")))
	switch {
	case !found:
		return rec, "", jsonapi.NotFound("system not found")
	case rec.Type != "bundle":
		return rec, "", jsonapi.BadRequest("not an evidence bundle")
	case !canManageAppliance(c.User, rec, servitorIsAdmin(c.R)):
		return rec, "", jsonapi.Forbidden("only the bundle's owner can move it")
	}
	return rec, owner, nil
}

// handleBundleMoveTargets lists the stores a bundle can be moved to (evidence
// on, and Full Access for its owner), one row
// each, for the page's ActionList.
func (T *Servitor) handleBundleMoveTargets(c *jsonapi.Call) (any, error) {
	rec, _, err := T.movableBundle(c)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	if drop, ok := evidenceDrop(); ok {
		for _, st := range drop.EvidenceStores(c.User) {
			desc := "Copies this bundle's files into a new folder of " + st.Name + ", which indexes it. Anyone who can reach " + st.Name + " can read the folder."
			if about := strings.TrimSpace(st.About); about != "" {
				desc = about + " " + desc
			}
			rows = append(rows, map[string]any{"Label": st.Name, "Desc": desc, "store": st.Store, "bundle": rec.ID})
		}
	}
	return rows, nil
}

func (T *Servitor) handleBundleMove(c *jsonapi.Call) (any, error) {
	rec, owner, err := T.movableBundle(c)
	if err != nil {
		return nil, err
	}
	// A row answers only for its own store, so the other rows stay idle.
	store := strings.TrimSpace(c.R.URL.Query().Get("store"))
	m, ok := currentBundleMove(bundleMoveKey(owner, rec.ID))
	if !ok || (store != "" && m.store != store) {
		return map[string]any{}, nil
	}
	return m.status(), nil
}

// handleBundleMoveStart starts moving a bundle into a store, or with ?cancel=
// stops the move under way.
func (T *Servitor) handleBundleMoveStart(c *jsonapi.Call, _ struct{}) (any, error) {
	rec, owner, err := T.movableBundle(c)
	if err != nil {
		return nil, err
	}
	key := bundleMoveKey(owner, rec.ID)
	q := c.R.URL.Query()
	if q.Get("cancel") != "" {
		if m, ok := currentBundleMove(key); ok {
			m.cancel()
		}
		return map[string]any{"ok": true}, nil
	}
	msg, err := T.startBundleMove(key, owner, c.User, rec, strings.TrimSpace(q.Get("store")))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"running": true, "message": msg}, nil
}

// startBundleMove checks the move can start and starts it in the background.
func (T *Servitor) startBundleMove(key, owner, userID string, rec Appliance, store string) (string, error) {
	drop, ok := evidenceDrop()
	if !ok {
		return "", Error("file stores are not available on this server")
	}
	var target appstores.EvidenceStore
	for _, st := range drop.EvidenceStores(userID) {
		if st.Store == store {
			target = st
		}
	}
	if target.Store == "" {
		return "", Error("that is not a file store indexing evidence that you can add to")
	}
	if rec.BundleState == bundleStateStaging || rec.BundleState == bundleStateIngesting {
		return "", Error("this bundle is still being uploaded or ingested: move it once that has finished")
	}
	b := bundle.Open(owner, rec.ID)
	files := b.Index()
	if len(files) == 0 {
		return "", Error("this bundle has no ingested files to move")
	}
	ctx, cancel := context.WithCancel(AppContext())
	m := &bundleMove{store: target.Store, storeName: target.Name, total: len(files), started: time.Now(), cancel: cancel}
	for _, bf := range files {
		if bf.Format != bundle.FormatBinary && bf.Format != bundle.FormatArchive {
			m.totalBytes += bf.Bytes
		}
	}
	bundleMoves.Lock()
	if prev, running := bundleMoves.at[key]; running {
		prev.mu.Lock()
		busy := prev.ended.IsZero()
		prev.mu.Unlock()
		if busy {
			bundleMoves.Unlock()
			cancel()
			return "", Error("this bundle is already being moved")
		}
	}
	bundleMoves.at[key] = m
	bundleMoves.Unlock()
	go m.run(ctx, drop, b, files, userID, rec.Name)
	return "Moving " + rec.Name + " to " + target.Name, nil
}

// run copies the files and records how it went.
func (m *bundleMove) run(ctx context.Context, drop appstores.EvidenceDrop, b bundle.Bundle, files []bundle.File, userID, name string) {
	defer m.cancel()
	var carried, empty []string
	folder, err := drop.AddEvidenceFolder(ctx, userID, m.store, name, m.totalBytes, func(dir string) error {
		for _, bf := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			wrote, err := exportBundleFile(b, bf, dir)
			if err != nil {
				return err
			}
			m.mu.Lock()
			m.files++
			if wrote {
				m.bytes += bf.Bytes
			}
			m.mu.Unlock()
			if wrote {
				carried = append(carried, bf.Path)
			} else {
				empty = append(empty, bf.Path)
			}
		}
		return nil
	})
	var outcome string
	switch {
	case ctx.Err() != nil:
		outcome = "Stopped. Nothing was added to " + m.storeName + "."
	case err != nil:
		outcome = "Not moved: " + err.Error() + ". Nothing was added to " + m.storeName + "."
	default:
		outcome = fmt.Sprintf("Moved to %s, folder %s: %d files, %s. The store is indexing it now; give an agent the store under Sources to work from it. This bundle is unchanged, so delete it once you no longer need it.",
			m.storeName, folder, len(carried), HumanSize(m.totalBytes))
		if len(empty) > 0 {
			outcome += fmt.Sprintf(" %d file(s) had no stored text to copy (binary, or an archive the ingest could not open): %s. Upload the originals into the folder if you still have them.",
				len(empty), strings.Join(firstN(empty, 5), ", "))
		}
	}
	Log("[servitor] bundle %s/%s move to %s: %s", b.Owner, b.ID, m.store, outcome)
	m.mu.Lock()
	m.ended, m.outcome = time.Now(), outcome
	m.mu.Unlock()
}

// exportBundleFile writes one bundle file under dir at its bundle path, and
// reports whether it had any stored text. One with none leaves no file.
func exportBundleFile(b bundle.Bundle, bf bundle.File, dir string) (bool, error) {
	rel := bundle.NormPath(bf.Path)
	dest := filepath.Join(dir, filepath.FromSlash(rel))
	if rel == "" || !strings.HasPrefix(dest, filepath.Clean(dir)+string(filepath.Separator)) {
		return false, fmt.Errorf("a bundle file has a path that leaves the folder: %q", bf.Path)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, err
	}
	wrote, err := b.Export(rel, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || !wrote {
		os.Remove(dest)
	}
	return wrote, err
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
}
