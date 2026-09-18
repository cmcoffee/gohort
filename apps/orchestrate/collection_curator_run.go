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
//	GET  .../curate  → what it is bound to, and how the last sync went
//	POST .../curate  → sync now
//
// The sync runs on a context DETACHED from the request. A sync over a large
// space outlives the click that started it, and one that dies when the browser
// gives up would leave the ledger half-written and the next run re-pulling
// everything it had already copied.
func (T *OrchestrateApp) handleCollectionCurate(w http.ResponseWriter, r *http.Request, user string, c Collection) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"sources": T.curatedStatusFor(user, c),
			"every":   curatorIntervalMin(),
		})
	case http.MethodPost:
		if len(c.CuratedFrom) == 0 {
			http.Error(w, "this collection is not a copy of anything — attach a source to curate it from first", http.StatusBadRequest)
			return
		}
		lines := T.curateCollectionAll(context.WithoutCancel(r.Context()), user, c)
		writeJSON(w, map[string]any{"ok": true, "results": lines})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
