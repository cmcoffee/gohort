package orchestrate

// When the curator runs, and over what.
//
// The unit is one binding — this collection, from that item of that source —
// rather than one collection, because a collection mirroring two spaces has
// two independent syncs and one of them failing should not stop the other.
//
// An interval rather than a trigger. There is no event to hang this on: the
// whole point is that the source changes without telling us, which is why a
// copy goes stale in the first place. A source that CAN tell us would be a
// better design and is not what any of them do.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const tuneCuratorIntervalMin = "tune_collection_curator_interval_min"

func init() {
	RegisterTunable(TunableSpec{
		App: "/orchestrate",
		Key: tuneCuratorIntervalMin, Category: "Limits",
		Label: "Collection curator interval (minutes)",
		Help: "How often a curated collection is brought back in step with the source it copies. " +
			"Every document the source says is unchanged is skipped without being fetched, so a sync over a large space is mostly free. 0 turns scheduled syncing off, leaving the manual run.",
		Kind: KindInt, Default: 360, Min: 0, Max: 10080,
	})
	RegisterMaintenanceFunc("Housekeeping", "collections_curate", "Sync curated collections",
		"Bring every curated collection back in step with the source it copies, now, instead of waiting for the interval.",
		func(ctx context.Context) int { return curateEveryone(ctx) })
}

func curatorIntervalMin() int { return TuneInt(tuneCuratorIntervalMin) }

// curateGuard serializes syncs per collection. Two runs over one collection
// would both enumerate, both decide the same documents were missing, and both
// ingest them — and the interval tick landing on top of a manual run is not
// hypothetical, it is what happens the moment somebody clicks Sync while a
// schedule is due.
var curateGuard sync.Map // collection id -> *sync.Mutex

func curateLock(collectionID string) *sync.Mutex {
	m, _ := curateGuard.LoadOrStore(collectionID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// curateCollectionAll syncs every source a collection is bound to, serialized
// against any other run over the same collection. Returns one line per
// binding, for a caller with somewhere to show it.
func (T *OrchestrateApp) curateCollectionAll(ctx context.Context, user string, c Collection) []string {
	if len(c.CuratedFrom) == 0 {
		return nil
	}
	lock := curateLock(c.ID)
	lock.Lock()
	defer lock.Unlock()

	udb := UserDB(T.DB, user)
	chunkDB := T.collectionDB(c)
	var lines []string
	for _, binding := range c.CuratedFrom {
		src, ok := ReferenceSourceByKind(binding.Kind)
		if !ok {
			// A source that is no longer connected leaves the copy alone and
			// says so. Silently skipping would read as a sync that found
			// nothing to do, which is the one thing it must not look like.
			lines = append(lines, fmt.Sprintf("%s: not connected — the copy is left as it is", bindingLabel(binding)))
			continue
		}
		res, err := curateCollection(ctx, udb, chunkDB, curateSpec{
			User: user, Collection: c, Source: src, Item: binding.Item,
		})
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", bindingLabel(binding), err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", bindingLabel(binding), res.String()))
	}
	return lines
}

func bindingLabel(b CuratedSource) string {
	if l := strings.TrimSpace(b.Label); l != "" {
		return l
	}
	return b.Kind + " · " + b.Item
}

// curateEveryone syncs every curated collection of every user, now. Returns
// how many collections were synced, for the maintenance panel.
//
// The context is the maintenance run's, so progress reported from inside
// curateCollection lands on the panel that started it.
func curateEveryone(ctx context.Context) int {
	app, ok := FindAgent("orchestrate")
	if !ok {
		return 0
	}
	T, ok := app.(*OrchestrateApp)
	if !ok || T.DB == nil {
		return 0
	}
	ran := 0
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			break
		}
		udb := UserDB(T.DB, u.Username)
		for _, c := range ListCollections(udb, u.Username) {
			if len(c.CuratedFrom) == 0 {
				continue
			}
			T.curateCollectionAll(ctx, u.Username, c)
			ran++
		}
	}
	return ran
}

// curatedStatus is what a collection's sync state looks like to a person: when
// it last ran, how it went, and how many documents it is holding from each
// source.
type curatedStatus struct {
	Label    string    `json:"label"`
	Docs     int       `json:"docs"`
	LastSync time.Time `json:"last_sync,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
}

func (T *OrchestrateApp) curatedStatusFor(user string, c Collection) []curatedStatus {
	udb := UserDB(T.DB, user)
	out := make([]curatedStatus, 0, len(c.CuratedFrom))
	for _, b := range c.CuratedFrom {
		l := loadCuratedLedger(udb, c.ID, b.Kind, b.Item)
		out = append(out, curatedStatus{
			Label:    bindingLabel(b),
			Docs:     len(l.Docs),
			LastSync: l.LastSync,
			Outcome:  l.LastOutcome,
		})
	}
	return out
}

// handleCollectionCurate serves the curated-source routes for one collection:
//
//	GET  .../curate      → the binding, as a form record, plus a status line
//	POST .../curate      → save the binding
//	POST .../curate/run  → sync now, answering the shape a Test button reads
//
// The binding travels as ONE value per row ("kind\x1fitem") rather than two
// fields. A source and an item within it are not independent choices — picking
// a space from another server's list would name nothing — so offering them as
// two controls would invite exactly one wrong answer and nothing else.
func (T *OrchestrateApp) handleCollectionCurate(w http.ResponseWriter, r *http.Request, user string, c Collection, run bool) {
	if run {
		T.handleCollectionCurateRun(w, r, user, c)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows := make([]map[string]string, 0, len(c.CuratedFrom))
		for _, b := range c.CuratedFrom {
			rows = append(rows, map[string]string{"source": b.Value()})
		}
		writeJSON(w, map[string]any{
			"curated_from": rows,
			"status":       T.curatedStatusLine(user, c),
		})
	case http.MethodPost:
		var body struct {
			CuratedFrom []struct {
				Source string `json:"source"`
			} `json:"curated_from"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		values := make([]string, 0, len(body.CuratedFrom))
		for _, row := range body.CuratedFrom {
			values = append(values, row.Source)
		}
		c.BindCuratedFrom(user, values)
		saveCollection(UserDB(T.DB, user), c)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCollectionCurateRun syncs now and answers in the shape a Test button
// reads: {ok, message} or {ok:false, error}.
//
// The sync runs on a context DETACHED from the request. A sync over a large
// space outlives the click that started it, and one that died when the browser
// gave up would leave the ledger half-written and the next run re-pulling
// everything it had already copied.
func (T *OrchestrateApp) handleCollectionCurateRun(w http.ResponseWriter, r *http.Request, user string, c Collection) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(c.CuratedFrom) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "this collection is not a copy of anything yet — pick a source above and save"})
		return
	}
	lines := T.curateCollectionAll(context.WithoutCancel(r.Context()), user, c)
	// Every line, not a count. A sync over two sources where one worked and one
	// could not reach its server is the case this exists to make visible, and a
	// tally of "1 of 2" says which half only by arithmetic.
	writeJSON(w, map[string]any{"ok": true, "message": strings.Join(lines, "\n")})
}

// curatedStatusLine renders the sync state for the form to print back.
func (T *OrchestrateApp) curatedStatusLine(user string, c Collection) string {
	st := T.curatedStatusFor(user, c)
	if len(st) == 0 {
		return "Not a copy of anything yet. Pick a source below, and this collection is kept in step with it: edits re-pulled, deletions retired."
	}
	var b strings.Builder
	for i, s := range st {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s.Label + " — " + pluralDocs(s.Docs))
		if s.LastSync.IsZero() {
			b.WriteString(", never synced")
		} else {
			b.WriteString(", last synced " + s.LastSync.In(UserLocation(user)).Format("2 Jan 15:04"))
			if s.Outcome != "" {
				b.WriteString(" (" + s.Outcome + ")")
			}
		}
	}
	return b.String()
}

func pluralDocs(n int) string {
	if n == 1 {
		return "1 document"
	}
	return fmt.Sprintf("%d documents", n)
}

// --- the interval sweep ------------------------------------------------------

// curatorSweepOnce guards the loop so it starts exactly once, whatever else
// calls Routes.
var curatorSweepOnce sync.Once

// curatorSweepTick is how often the sweep WAKES, not how often a collection
// syncs. Short enough that a changed interval takes effect soon, long enough
// that waking costs nothing.
const curatorSweepTick = 5 * time.Minute

// startCuratorSweep runs the interval. Each tick syncs every curated collection
// whose last sync is older than the configured interval.
//
// The tick is a fixed short period and the DECISION is per collection, rather
// than a ticker set to the interval itself. The interval is a tunable an admin
// can change at any time, and a ticker built once at startup would keep the old
// period until a restart — which is the shape of bug where somebody sets it to
// an hour, sees nothing happen for six, and concludes the feature is broken.
func startCuratorSweep(app *OrchestrateApp) {
	curatorSweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(curatorSweepTick)
			defer ticker.Stop()
			for range ticker.C {
				app.sweepCuratedCollections(context.Background())
			}
		}()
	})
}

// lastCuratedSync tracks per-collection interval firing in memory.
//
// Deliberately not persisted, matching the ledger's own LastSync being a RECORD
// rather than a schedule: after a restart the worst case is one extra sync,
// which fetches only what actually changed and is therefore nearly free, and a
// persisted timestamp would add a write to a path whose whole job is to be
// cheap when there is nothing to do.
var lastCuratedSync sync.Map // collection id -> time.Time

func (T *OrchestrateApp) sweepCuratedCollections(ctx context.Context) {
	every := curatorIntervalMin()
	if every <= 0 {
		return // scheduled syncing is off; the manual run still works
	}
	cutoff := time.Now().Add(-time.Duration(every) * time.Minute)
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		udb := UserDB(T.DB, u.Username)
		for _, c := range ListCollections(udb, u.Username) {
			if len(c.CuratedFrom) == 0 {
				continue
			}
			if last, ok := lastCuratedSync.Load(c.ID); ok {
				if at, isTime := last.(time.Time); isTime && at.After(cutoff) {
					continue
				}
			}
			// Stamped BEFORE the run, not after. A sync that takes longer than
			// the interval would otherwise be due again the moment it finished,
			// and the per-collection lock would turn that into a queue of
			// waiting syncs rather than a skipped one.
			lastCuratedSync.Store(c.ID, time.Now())
			T.curateCollectionAll(ctx, u.Username, c)
		}
	}
}
