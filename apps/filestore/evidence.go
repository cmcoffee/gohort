package filestore

// Evidence: a store's folders indexed the way an evidence bundle is.
//
// A store an agent greps is quick to set up and reads files as they are. An
// evidence bundle (core/bundle) is more: every file is indexed (format, line
// count, time span, emitting host, severity counts), timestamps are parsed, a
// timeline merges files in time order, and the indexed copy is encrypted at
// rest. That used to need a servitor bundle system per dump, uploaded by hand
// (docs/design/servitor-systems-model.md). With "Index folders as evidence" on, each
// subfolder of the store (a case, a run, a dump) is indexed into the bundle
// store as it arrives, and an attached agent gets summarize_<slug> and
// timeline_<slug> next to list, search and read.
//
// The folder is never changed: IngestFrom reads it and writes only the index.
// Each folder is its own bundle, so two cases in one store never mix, and the
// index follows the folder: changed, it is indexed again; deleted, its index is
// wiped; the switch turned off or the store removed, every index of it goes.
//
// When a folder is indexed:
//   - when files arrive by upload or pull (noteChanged);
//   - on a 15-minute sweep, for whatever arrived some other way (a command's
//     download, scp, a cron job), which also wipes the index of a folder gone;
//   - when an agent asks about a folder that is not indexed, or has changed
//     since: the call indexes it first, moving to the background past a
//     minute (bgrun) so a large folder does not hold the turn.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

const (
	// evidenceTable records, per store folder, what its index was built from.
	evidenceTable = "evidence_index"
	// evidenceSweepEvery is how often the sweep looks for folders that changed
	// or went away. The same period as the temporary-store sweep.
	evidenceSweepEvery = 15 * time.Minute
	// evidenceOutcomeKept is how long a finished run's outcome stays to read.
	evidenceOutcomeKept = 30 * time.Minute
)

// evidenceState is what a folder's index was built from, and how it went.
type evidenceState struct {
	Fingerprint string    `json:"fingerprint"`
	IndexedAt   time.Time `json:"indexed_at"`
	Files       int       `json:"files"`
	TextFiles   int       `json:"text_files"`
	Unopened    int       `json:"unopened"`
	Skipped     int       `json:"skipped"`
	Lines       int       `json:"lines"`
	Bytes       int64     `json:"bytes"`
	Truncated   bool      `json:"truncated,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// evidenceBundle is one store folder's index. The owner names the store, so a
// store's indexes live apart from every person's bundles and from each other.
func evidenceBundle(slug, folder string) bundle.Bundle {
	return bundle.Open("store:"+slug, folder)
}

func evidenceKey(slug, folder string) string { return slug + "/" + folder }

// folderPrint is a cheap reading of a folder that changes when its contents
// do: how many files, how many bytes, and the newest modification. Read off a
// walk of stats, never file contents. files is returned for progress.
func folderPrint(dir string) (print string, files int, err error) {
	var bytes int64
	var newest time.Time
	err = filepath.Walk(dir, func(_ string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		files++
		bytes += info.Size()
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return fmt.Sprintf("%d:%d:%d", files, bytes, newest.UnixNano()), files, err
}

// evidenceFolder resolves a folder name an agent or a page sent: one
// subfolder of the store, by name. Not a path: the index is per case.
func evidenceFolder(st Store, folder string) (string, string, error) {
	folder = strings.TrimSpace(folder)
	if folder == "" || strings.ContainsAny(folder, `/\`) || strings.HasPrefix(folder, ".") {
		return "", "", Error("name one folder of the " + st.Name + " store, as list_" + st.Slug + " shows it, not a path")
	}
	dir, err := SubRoot(st.Path, folder)
	if err != nil {
		return "", "", err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", "", Error("the " + st.Name + " store has no folder named " + folder)
	}
	return folder, dir, nil
}

// evidenceRun is one folder being indexed.
type evidenceRun struct {
	slug, folder string
	total        int // files in the folder when the run started, for progress
	cancel       context.CancelFunc
	done         chan struct{}
	started      time.Time
	ended        time.Time
	result       string
	err          error
}

// evidenceRegistry is the folders this server is indexing, one run per folder,
// and the last outcome of each for a while after.
type evidenceRegistry struct {
	mu   sync.Mutex
	runs map[string]*evidenceRun
}

var evidenceRuns = &evidenceRegistry{runs: map[string]*evidenceRun{}}

func (r *evidenceRegistry) get(key string) (*evidenceRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[key]
	if ok && !run.ended.IsZero() && time.Since(run.ended) > evidenceOutcomeKept {
		delete(r.runs, key)
		return nil, false
	}
	return run, ok
}

// start indexes one folder in the background, unless it is already being
// indexed, in which case the running one is returned. Not on any request's
// context: the index outlives the click or the call that asked for it.
func (r *evidenceRegistry) start(db Database, st Store, folder, dir string) *evidenceRun {
	key := evidenceKey(st.Slug, folder)
	if cur, ok := r.get(key); ok && cur.ended.IsZero() {
		return cur
	}
	_, total, _ := folderPrint(dir) // outside the lock: a walk is slow
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.runs[key]; ok && cur.ended.IsZero() {
		return cur
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &evidenceRun{slug: st.Slug, folder: folder, total: total, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	r.runs[key] = run
	go func() {
		defer close(run.done)
		defer cancel()
		res, err := indexEvidenceFolder(ctx, db, st, folder, dir)
		r.mu.Lock()
		run.result, run.err, run.ended = res, err, time.Now()
		r.mu.Unlock()
	}()
	return run
}

// progress is "1,200 of 8,400 files - 41s" while running.
func (run *evidenceRun) progress() string {
	done := evidenceBundle(run.slug, run.folder).FileCount()
	return fmt.Sprintf("indexing %s: %s of %s files - %s", run.folder, commaN(int64(done)), commaN(int64(run.total)), shortSince(run.started))
}

// status is {"progress"} while it runs or {"outcome"} once ended.
func (run *evidenceRun) status() map[string]any {
	evidenceRuns.mu.Lock()
	ended, res, err := run.ended, run.result, run.err
	evidenceRuns.mu.Unlock()
	if ended.IsZero() {
		return map[string]any{"progress": run.progress()}
	}
	if err != nil {
		return map[string]any{"outcome": "Failed: " + err.Error()}
	}
	return map[string]any{"outcome": res}
}

// indexEvidenceFolder builds one folder's index and records it. The print is
// read BEFORE the ingest, so a file that lands during it reads as a change
// next time and is indexed then, rather than being missed under a print taken
// after it.
func indexEvidenceFolder(ctx context.Context, db Database, st Store, folder, dir string) (string, error) {
	print, _, _ := folderPrint(dir)
	stats, err := evidenceBundle(st.Slug, folder).IngestFrom(ctx, dir)
	state := evidenceState{Fingerprint: print, IndexedAt: time.Now(), Files: stats.Files, TextFiles: stats.TextFiles,
		Unopened: stats.Unopened, Skipped: stats.Skipped, Lines: stats.Lines, Bytes: stats.Bytes, Truncated: stats.Truncated}
	if err != nil {
		// A stopped or failed run leaves a partial index; it is wiped so a
		// reader never takes half a folder for the whole, and the empty print
		// makes the next look index it again.
		evidenceBundle(st.Slug, folder).Wipe()
		state = evidenceState{Error: err.Error(), IndexedAt: time.Now()}
		db.Set(evidenceTable, evidenceKey(st.Slug, folder), state)
		if ctx.Err() != nil {
			return "", Error("stopped; " + folder + " is not indexed")
		}
		return "", err
	}
	db.Set(evidenceTable, evidenceKey(st.Slug, folder), state)
	return "Indexed " + folder + ": " + state.describe(), nil
}

// describe is one line for a page or an agent: what the index holds.
func (s evidenceState) describe() string {
	out := fmt.Sprintf("%s files (%s read as text), %s lines, %s", commaN(int64(s.Files)), commaN(int64(s.TextFiles)), commaN(int64(s.Lines)), HumanSize(s.Bytes))
	if s.Unopened > 0 {
		out += fmt.Sprintf(", %d archive(s) not opened", s.Unopened)
	}
	if s.Truncated {
		out += fmt.Sprintf(", ONLY THE FIRST %s: the folder is over the index's limit, the rest is not indexed", HumanSize(bundle.MaxBytes))
	}
	return out
}

// evidenceStateOf reads a folder's recorded state; ok is false when it has
// never been indexed.
func evidenceStateOf(db Database, slug, folder string) (evidenceState, bool) {
	var s evidenceState
	ok := db.Get(evidenceTable, evidenceKey(slug, folder), &s)
	return s, ok
}

// evidenceCurrent reports whether a folder's index matches the folder now.
func evidenceCurrent(db Database, slug, folder, dir string) (evidenceState, bool) {
	s, ok := evidenceStateOf(db, slug, folder)
	if !ok || s.Error != "" {
		return s, false
	}
	print, _, err := folderPrint(dir)
	return s, err == nil && print == s.Fingerprint
}

// wipeEvidence removes one folder's index and its record.
func wipeEvidence(db Database, slug, folder string) {
	if run, ok := evidenceRuns.get(evidenceKey(slug, folder)); ok && run.ended.IsZero() {
		run.cancel()
		<-run.done
	}
	evidenceBundle(slug, folder).Wipe()
	db.Unset(evidenceTable, evidenceKey(slug, folder))
}

// wipeStoreEvidence removes every index of a store: its switch was turned off,
// or it was removed.
func wipeStoreEvidence(db Database, slug string) int {
	n := 0
	for _, key := range db.Keys(evidenceTable) {
		s, folder, ok := strings.Cut(key, "/")
		if ok && s == slug {
			wipeEvidence(db, slug, folder)
			n++
		}
	}
	return n
}

// noteChanged indexes a folder that something just wrote into, when its store
// indexes evidence. Called after an upload or a pull lands.
func noteChanged(db Database, st Store, folder string) {
	if !st.Evidence {
		return
	}
	if folder, dir, err := evidenceFolder(st, folder); err == nil {
		evidenceRuns.start(db, st, folder, dir)
	}
}

// sweepEvidence brings every evidence store's indexes in line with its
// folders: a changed or new folder is indexed (one at a time, so the sweep
// never runs a dozen walks at once), an index whose folder is gone is wiped,
// and so is every index of a store that no longer indexes evidence.
func sweepEvidence(ctx context.Context, db Database) {
	stores := map[string]Store{}
	for _, st := range ListStores(db) {
		stores[st.Slug] = st
	}
	for _, key := range db.Keys(evidenceTable) {
		slug, folder, ok := strings.Cut(key, "/")
		if !ok {
			db.Unset(evidenceTable, key)
			continue
		}
		st, ok := stores[slug]
		if !ok || !st.Evidence {
			wipeEvidence(db, slug, folder)
			continue
		}
		if _, _, err := evidenceFolder(st, folder); err != nil {
			wipeEvidence(db, slug, folder)
		}
	}
	for _, st := range stores {
		if !st.Evidence {
			continue
		}
		folders, err := ListFolders(st.Path)
		if err != nil {
			continue
		}
		for _, f := range folders {
			if ctx.Err() != nil {
				return
			}
			folder, dir, err := evidenceFolder(st, f.Name)
			if err != nil {
				continue
			}
			if _, current := evidenceCurrent(db, st.Slug, folder, dir); current {
				continue
			}
			<-evidenceRuns.start(db, st, folder, dir).done
		}
	}
}

// sweepEvidenceLoop runs sweepEvidence on a timer, once shortly after start so
// a restart does not wait a quarter hour to catch up.
func (T *FileStoreApp) sweepEvidenceLoop() {
	time.Sleep(time.Minute)
	sweepEvidence(context.Background(), T.DB)
	tick := time.NewTicker(evidenceSweepEvery)
	defer tick.Stop()
	for range tick.C {
		sweepEvidence(context.Background(), T.DB)
	}
}

// --- the agent's tools -------------------------------------------------------

// evidenceToolNames are the tools a store indexing evidence adds.
func evidenceToolNames(slug string) []string {
	return []string{"summarize_" + slug, "timeline_" + slug}
}

// evidenceToolDefs are the summary and timeline tools for a store indexing
// evidence. Each takes the folder, indexes it first when it is not indexed or
// has changed, and then answers through the evidence bundle's own tools, so a
// store's evidence reads exactly as a servitor bundle does.
func (s storeSource) evidenceToolDefs(sess *ToolSession, st Store) []AgentToolDef {
	if !st.Evidence {
		return nil
	}
	folderParam := ToolParam{Type: "string", Description: "The folder (case) to read, by name, as list_" + st.Slug + " shows it."}
	timeParams := map[string]ToolParam{
		"folder":    folderParam,
		"pattern":   {Type: "string", Description: TimelinePatternHelp},
		"exclude":   {Type: "string", Description: TimelineExcludeHelp},
		"glob":      {Type: "string", Description: "Optional path filter limiting which files are merged, e.g. \"*scheduler*\". Empty merges every file that carries timestamps."},
		"since":     {Type: "string", Description: "Earliest timestamp, e.g. \"2026-03-14 02:00:00\". Strongly recommended."},
		"until":     {Type: "string", Description: "Latest timestamp, same format."},
		"max_lines": {Type: "integer", Description: fmt.Sprintf("Maximum lines to return (default %d).", bundle.MaxTimelineLines)},
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "summarize_" + st.Slug,
				Description: fmt.Sprintf("Overview of one folder of the %q file store, from its evidence index: how many files, what period they cover, which hosts wrote them, which files are noisiest, and which are present but unread (binaries, archives nobody opened). Call it first on a folder: it tells you what you are looking at and where to search, without reading any log content. A folder not indexed yet is indexed first.", st.Name),
				Parameters:  map[string]ToolParam{"folder": folderParam},
				Required:    []string{"folder"},
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return s.answerFromEvidence(sess, st, StringArg(args, "folder"), "bundle_summary", nil)
			},
		},
		{
			Tool: Tool{
				Name:        "timeline_" + st.Slug,
				Description: fmt.Sprintf("Merge lines from SEVERAL files of one folder of the %q file store into one time-ordered sequence: what happened across the whole system in a window, in order, which no per-file search can show. Narrow it with since and until, and with pattern to follow one thing (a request id, a hostname) through every file in order: an unbounded timeline over a large folder is truncated and tells you little. A folder not indexed yet is indexed first.", st.Name),
				Parameters:  timeParams,
				Required:    []string{"folder"},
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return s.answerFromEvidence(sess, st, StringArg(args, "folder"), "bundle_timeline", args)
			},
		},
	}
}

// answerFromEvidence answers one evidence question about a folder, indexing it
// first when its index is missing or stale. Indexing that runs past a minute
// moves to the background and reports back (bgrun), as a long store command
// does; the agent is told the answer is coming rather than given a stale one.
func (s storeSource) answerFromEvidence(sess *ToolSession, st Store, folder, tool string, args map[string]any) (string, error) {
	folder, dir, err := evidenceFolder(st, folder)
	if err != nil {
		return "", err
	}
	db := s.app.DB
	answer := func(ctx context.Context) (string, error) {
		state, _ := evidenceStateOf(db, st.Slug, folder)
		b := evidenceBundle(st.Slug, folder)
		for _, td := range BundleTools(ctx, b.Owner, b.ID) {
			if td.Tool.Name == tool {
				out, err := td.Handler(ctx, args)
				if err != nil {
					return "", err
				}
				head := fmt.Sprintf("Evidence index of %s in %s (indexed %s): %s\n\n", folder, st.Name, state.IndexedAt.Format("Jan 2 15:04"), state.describe())
				return head + out, nil
			}
		}
		return "", fmt.Errorf("no evidence tool %s", tool)
	}
	if _, current := evidenceCurrent(db, st.Slug, folder, dir); current {
		return answer(sess.Context())
	}
	return bgrun.BackgroundRun{Tool: tool + "_" + st.Slug, Label: "indexing " + folder + " in " + st.Name,
		Run: func(work *ToolSession) (string, error) {
			run := evidenceRuns.start(db, st, folder, dir)
			act := bgrun.RunActivityFrom(work.Context())
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			seen := -1
			for {
				select {
				case <-run.done:
					evidenceRuns.mu.Lock()
					err := run.err
					evidenceRuns.mu.Unlock()
					if err != nil {
						return "", fmt.Errorf("could not index %s: %w", folder, err)
					}
					return answer(work.Context())
				case <-work.Context().Done():
					// The index goes on without the call: another look, or the
					// sweep, will find it done.
					return "", Error("stopped waiting; " + folder + " is still being indexed")
				case <-tick.C:
					// Output only when more files are indexed than last time:
					// that is the run working, and a stuck index then meets
					// the no-output limit like any stuck command.
					if act == nil {
						continue
					}
					if n := evidenceBundle(st.Slug, folder).FileCount(); n > seen {
						seen = n
						fmt.Fprintln(act, run.progress())
					} else {
						act.Note(run.progress())
					}
				}
			}
		}}.Start(sess)
}

// --- the admin page ----------------------------------------------------------

// evidenceItems is the store page's list of folders and the state of each
// one's index.
func (T *FileStoreApp) evidenceItems(st Store) []map[string]any {
	out := []map[string]any{}
	if !st.Evidence {
		return out
	}
	folders, err := ListFolders(st.Path)
	if err != nil {
		return out
	}
	for _, f := range folders {
		folder, dir, err := evidenceFolder(st, f.Name)
		if err != nil {
			continue
		}
		desc := "not indexed yet"
		state, ok := evidenceStateOf(T.DB, st.Slug, folder)
		switch _, current := evidenceCurrent(T.DB, st.Slug, folder, dir); {
		case ok && state.Error != "":
			desc = "the last index failed: " + state.Error
		case current:
			desc = "indexed " + state.IndexedAt.Format("Jan 2 15:04") + ": " + state.describe()
		case ok:
			desc = "changed since it was indexed " + state.IndexedAt.Format("Jan 2 15:04") + "; indexed again on the next sweep or when an agent asks"
		}
		out = append(out, map[string]any{"item": folder, "desc": fmt.Sprintf("%d files, %s", f.Files, HumanSize(f.Bytes)), "history": desc})
	}
	return out // newest first, as ListFolders sorts them
}

// handleEvidence serves the store page's evidence list: the folders, Index
// now, its progress and Stop. Admin only, like the rest of the store page.
func (T *FileStoreApp) handleEvidence() http.HandlerFunc {
	key := func(c *jsonapi.Call, st Store) string {
		return evidenceKey(st.Slug, strings.TrimSpace(c.R.URL.Query().Get("item")))
	}
	items := jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		st, err := T.adminStore(c)
		if err != nil {
			return nil, err
		}
		return T.evidenceItems(st), nil
	})
	progress := jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		st, err := T.adminStore(c)
		if err != nil {
			return nil, err
		}
		if run, ok := evidenceRuns.get(key(c, st)); ok {
			return run.status(), nil
		}
		return map[string]any{}, nil
	})
	cancel := jsonapi.Post(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
		st, err := T.adminStore(c)
		if err != nil {
			return nil, err
		}
		if run, ok := evidenceRuns.get(key(c, st)); ok {
			run.cancel()
		}
		return map[string]any{"ok": true}, nil
	})
	start := jsonapi.Post(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
		st, err := T.adminStore(c)
		if err != nil {
			return nil, err
		}
		if !st.Evidence {
			return nil, jsonapi.BadRequest(st.Name + " does not index folders as evidence: turn it on in its Settings first")
		}
		folder, dir, err := evidenceFolder(st, strings.TrimSpace(c.R.URL.Query().Get("item")))
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		evidenceRuns.start(T.DB, st, folder, dir)
		return map[string]any{"running": true, "message": "Indexing " + folder}, nil
	})
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/items"):
			items(w, r)
		case strings.HasSuffix(r.URL.Path, "/progress"):
			progress(w, r)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancel(w, r)
		default:
			start(w, r)
		}
	}
}
