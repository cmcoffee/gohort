// When the curator runs.
//
// Not on submission. A curator invoked per finding sees one finding, which is
// the same blindness the producer had — it could not tell that three reports
// from one investigation are one section, or that a later finding in the same
// batch supersedes an earlier one. Batching is not an optimization here; it is
// what makes the editorial judgments expressible at all.
//
// So: whichever comes first, a THRESHOLD (enough findings have piled up that
// there is a batch worth reasoning over) or an INTERVAL (a few findings should
// not sit unfiled forever because the threshold never gets reached).
package guides

import (
	"context"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	tuneCuratorThreshold = "tune_guides_curator_threshold"
	tuneCuratorInterval  = "tune_guides_curator_interval_min"
)

func init() {
	RegisterTunable(TunableSpec{
		App: "/guides",
		Key: tuneCuratorThreshold, Category: "Limits",
		Label: "Guide curator batch threshold",
		Help:  "Run the Guide Curator once this many findings are waiting for a user. 0 disables threshold firing, leaving only the interval.",
		Kind:  KindInt, Default: 5, Min: 0, Max: 100,
	})
	RegisterTunable(TunableSpec{
		App: "/guides",
		Key: tuneCuratorInterval, Category: "Limits",
		Label: "Guide curator interval (minutes)",
		Help:  "Run the Guide Curator for any user with waiting findings at least this often, even if the threshold was never reached. 0 disables interval firing.",
		Kind:  KindInt, Default: 60, Min: 0, Max: 1440,
	})
	// A manual trigger in the admin panel. Present because the two automatic
	// firings are both delayed by design: without this there is no way to see
	// what the curator does with a batch you just produced.
	RegisterMaintenanceFunc("guides_curate", "Run the Guide Curator",
		"Drain every user's pending guide findings now, instead of waiting for the batch threshold or interval.",
		func(ctx context.Context) int { return runCuratorForEveryone(ctx) })
}

func curatorThreshold() int { return TuneInt(tuneCuratorThreshold) }
func curatorInterval() time.Duration {
	return time.Duration(TuneInt(tuneCuratorInterval)) * time.Minute
}

// curatorGuard serializes runs per user. Two concurrent runs over one queue
// would both read the same pending findings and both file them — the threshold
// firing and the interval tick landing together is not hypothetical, it is what
// happens the moment a burst of findings arrives near an interval boundary.
var curatorGuard sync.Map // user -> *sync.Mutex

func curatorLock(user string) *sync.Mutex {
	m, _ := curatorGuard.LoadOrStore(user, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// lastCuratorRun tracks per-user interval firing in memory. Deliberately not
// persisted: after a restart the worst case is one extra run, which files the
// same findings it would have filed anyway, and a persisted timestamp would add
// a write on a path whose whole job is to be cheap.
var lastCuratorRun sync.Map // user -> time.Time

// maybeRunCurator fires a run if this user has crossed the threshold. Called
// after a finding is submitted; returns without blocking the submitter.
func (T *Guides) maybeRunCurator(user string) {
	n := curatorThreshold()
	if n <= 0 {
		return
	}
	udb := UserDB(T.DB, user)
	if udb == nil || len(udb.Keys(findingsTable)) < n {
		return
	}
	go T.runCuratorGuarded(AppContext(), user, "threshold")
}

// runCuratorGuarded runs the curator for one user under that user's lock, and
// swallows the "nothing pending" case so callers can fire freely.
func (T *Guides) runCuratorGuarded(ctx context.Context, user, why string) {
	mu := curatorLock(user)
	if !mu.TryLock() {
		return // a run is already in flight for this user; it will see the queue
	}
	defer mu.Unlock()
	run, err := T.RunCurator(ctx, user)
	lastCuratorRun.Store(user, time.Now())
	switch {
	case err != nil:
		Log("[guides.curator] %s run for %q failed: %v", why, user, err)
	case run.ID == "":
		// Nothing was pending. Not worth a log line — the interval sweep hits
		// this for every user with an empty queue.
	default:
		T.notifyDigest(user, run)
	}
}

// startCuratorLoop begins the interval sweep. Called once from Routes.
func (T *Guides) startCuratorLoop() {
	go func() {
		// A short initial delay so a restart does not run the curator before
		// the rest of the app (orchestrate, the agent registry) is up.
		time.Sleep(30 * time.Second)
		for {
			every := curatorInterval()
			if every <= 0 {
				// Interval firing disabled — keep the loop alive at a slow
				// poll so re-enabling the tunable takes effect without a
				// restart.
				time.Sleep(10 * time.Minute)
				continue
			}
			T.sweepCurator(AppContext())
			time.Sleep(every)
		}
	}()
}

// sweepCurator runs the curator for every user whose queue has waited longer
// than the interval.
func (T *Guides) sweepCurator(ctx context.Context) {
	every := curatorInterval()
	if every <= 0 || T.DB == nil {
		return
	}
	for _, u := range AuthListUsers(AuthDB()) {
		user := u.Username
		if user == "" {
			continue
		}
		udb := UserDB(T.DB, user)
		if udb == nil || len(udb.Keys(findingsTable)) == 0 {
			continue
		}
		if last, ok := lastCuratorRun.Load(user); ok {
			if t, isTime := last.(time.Time); isTime && time.Since(t) < every {
				continue
			}
		}
		T.runCuratorGuarded(ctx, user, "interval")
	}
}

// runCuratorForEveryone drains every user's queue now, ignoring the interval.
// Returns how many runs produced entries, for the maintenance panel.
func runCuratorForEveryone(ctx context.Context) int {
	app, ok := FindAgent("guides")
	if !ok {
		return 0
	}
	g, ok := app.(*Guides)
	if !ok || g.DB == nil {
		return 0
	}
	ran := 0
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username == "" {
			continue
		}
		before := len(g.pendingKeys(u.Username))
		if before == 0 {
			continue
		}
		g.runCuratorGuarded(ctx, u.Username, "manual")
		ran++
	}
	return ran
}

// pendingKeys is the cheap queue-depth probe used by the sweep and the
// maintenance runner.
func (T *Guides) pendingKeys(user string) []string {
	udb := UserDB(T.DB, user)
	if udb == nil {
		return nil
	}
	return udb.Keys(findingsTable)
}

// notifyDigest tells the user a run happened, but only when it did something
// they would want to know about. A run that filed two findings into an existing
// guide is routine; one that CREATED a guide or flagged a contradiction made a
// claim a human should see, and those are the two cases the spec singles out.
//
// Routine runs are still in the digest list — this is about what interrupts
// someone, not about what is recorded.
func (T *Guides) notifyDigest(user string, run CuratorRun) {
	counts := run.Counts()
	created, flagged := counts[OutcomeCreated], counts[OutcomeContradiction]
	if created == 0 && flagged == 0 {
		return
	}
	var msg string
	switch {
	case created > 0 && flagged > 0:
		msg = "The Guide Curator created a guide and flagged " + itoa(flagged) + " contradiction(s) for you to look at."
	case created > 0:
		msg = "The Guide Curator created a new guide from findings that fit nowhere existing."
	default:
		msg = "The Guide Curator flagged " + itoa(flagged) + " contradiction(s) between new findings and your guides."
	}
	NotifyUser(user, "Guides", msg)
}
