package orchestrate

// Run now: the collection's plan, carried out on the collection's behalf
// (docs/design/knowledge-one-curation.md).
//
//	GET  /api/collections/{id}/curate           the Run now row
//	POST /api/collections/{id}/curate           start (one run per collection)
//	GET  /api/collections/{id}/curate/progress  {"progress"} while it runs, {"outcome"} after
//	POST /api/collections/{id}/curate/stop      stop it
//
// What runs follows the curation chosen. Hand Curated and Auto-Curation run
// the plan itself (runPlan): the same run, once on the press or on the
// schedule. Curation by Agent runs the person's agent, briefed with the plan,
// with the tools it already has. A scheduled refresh starts the same run
// (collection_refresh.go). The run is on a context of its own, so closing the
// page does not stop it, and a page opened mid-run rejoins it. The plan's
// documents per run is the cap, and the run time limit ends it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// curateOutcomeKept is how long a finished run's outcome stays readable, so a
// page arriving after the end says how it went.
const curateOutcomeKept = 30 * time.Minute

// curateCountEvery is how often a running curation recounts the collection's
// documents for its progress line. A count reads every chunk of the
// collection, so it is not done per poll.
var curateCountEvery = 10 * time.Second

// curateRun is one run on a collection's behalf, going or recently ended: a
// curation, a search for sources to suggest, or adding the ones ticked.
type curateRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
	label   string // what it is doing: "Curating"
	before  int
	counted bool // docs is kept, and "N documents added" is part of its lines

	mu            sync.Mutex
	docs          int
	note          string // the run's own word on how far it has got
	reply         string
	err           error
	ended         time.Time
	stopRequested bool
}

type curateRegistry struct {
	mu   sync.Mutex
	runs map[string]*curateRun
}

var curations = &curateRegistry{runs: map[string]*curateRun{}}

func (r *curateRegistry) get(id string) (*curateRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return nil, false
	}
	run.mu.Lock()
	ended := run.ended
	run.mu.Unlock()
	if !ended.IsZero() && time.Since(ended) > curateOutcomeKept {
		delete(r.runs, id)
		return nil, false
	}
	return run, true
}

// start runs work for the collection c under key unless a run under it is
// already going, in which case that one is returned and started is false.
// label says what it is doing. count, when given, is the collection's
// document count, read every curateCountEvery for "N documents added". work
// may report how far it has got through note. The run is listed with the
// rest of the running work (the Monitor, the live pill), under the
// collection's owner, and stopped from there as from the collection's page.
func (r *curateRegistry) start(key string, c Collection, label string, before int, count func() int, work func(ctx context.Context, note func(string)) (string, error)) (run *curateRun, started bool) {
	id := key
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.runs[id]; ok {
		cur.mu.Lock()
		running := cur.ended.IsZero()
		cur.mu.Unlock()
		if running {
			return cur, false
		}
	}
	// Not the request's context: the run outlives the click that started it.
	ctx, cancel := context.WithTimeout(context.Background(), autofillTimeout())
	every := curateCountEvery
	run = &curateRun{cancel: cancel, done: make(chan struct{}), started: time.Now(), label: label,
		before: before, docs: before, counted: count != nil}
	if count == nil {
		count = func() int { return before }
	}
	var live *Run
	if o, ok := agenthostApp(); ok && strings.TrimSpace(c.Owner) != "" {
		ctx, live = o.runsRegistry().CreateCancellable(ctx, c.Owner, "", "")
		live.Describe("curation", curationRunName(c), label)
	}
	note := func(line string) {
		run.mu.Lock()
		run.note = line
		run.mu.Unlock()
		if live != nil {
			live.SetDetail(line)
		}
	}
	r.runs[id] = run
	go func() {
		defer close(run.done)
		defer cancel()
		// The recount stops before the run is marked done, so nothing of
		// the run is left going once it has ended.
		stop, counted := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(counted)
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					n := count()
					run.mu.Lock()
					run.docs = n
					run.mu.Unlock()
				}
			}
		}()
		reply, err := work(ctx, note)
		close(stop)
		<-counted
		n := count()
		run.mu.Lock()
		run.docs, run.reply, run.err, run.ended = n, reply, err, time.Now()
		if ctx.Err() == context.DeadlineExceeded {
			run.err = fmt.Errorf("it ran past the %s limit for one run", curateLimit(autofillTimeout()))
		}
		// Stopped from the running list rather than the collection's page.
		if errors.Is(ctx.Err(), context.Canceled) {
			run.stopRequested = true
		}
		status := RunStatusCompleted
		switch {
		case run.stopRequested:
			status = RunStatusCanceled
		case run.err != nil:
			status = RunStatusFailed
		}
		run.mu.Unlock()
		if live != nil {
			live.Complete(status)
		}
	}()
	return run, true
}

// curationRunName is how a curation of c is listed with the running work:
// the collection by name, or its app's name when an app owns it.
func curationRunName(c Collection) string {
	if strings.TrimSpace(c.OwningApp) != "" {
		return appRunLabel(c.Owner, c.OwningApp)
	}
	return "Knowledge: " + firstNonEmptyStr(strings.TrimSpace(c.Name), c.ID)
}

// stop asks the run to end. It ends at the curator's next step.
func (run *curateRun) stop() {
	run.mu.Lock()
	run.stopRequested = true
	run.mu.Unlock()
	run.cancel()
}

// added is how many documents the collection has gained since the run began.
// Never negative: a curator that removed more than it added has added none.
func (run *curateRun) added() int {
	if n := run.docs - run.before; n > 0 {
		return n
	}
	return 0
}

// status is {"progress"} while the run goes, non-empty for the whole of it so
// a page opened mid-run rejoins it, and {"outcome"} once it has ended.
func (run *curateRun) status() map[string]any {
	run.mu.Lock()
	defer run.mu.Unlock()
	added := ""
	if run.counted {
		added = fmt.Sprintf(", %d document%s added", run.added(), plural(run.added()))
	}
	if run.ended.IsZero() {
		line := run.label
		if n := strings.TrimSpace(run.note); n != "" {
			line += " - " + n
		}
		if run.counted {
			line += " - " + strings.TrimPrefix(added, ", ")
		}
		return map[string]any{"progress": line + " - " + curateElapsed(time.Since(run.started))}
	}
	took := curateElapsed(run.ended.Sub(run.started))
	switch {
	case run.stopRequested:
		return map[string]any{"outcome": "Stopped after " + took + added + "."}
	case run.err != nil:
		return map[string]any{"outcome": "Failed after " + took + added + ": " + run.err.Error()}
	}
	out := "Done in " + took + added + "."
	if reply := curateCut(run.reply, 600); reply != "" {
		out += " " + reply
	}
	return map[string]any{"outcome": out}
}

// curateElapsed is "41s" or "3m 05s".
func curateElapsed(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

// curateLimit is a run's time limit in words: "10 minutes".
func curateLimit(d time.Duration) string {
	if m := int(d / time.Minute); m >= 1 && d%time.Minute == 0 {
		return fmt.Sprintf("%d minute%s", m, plural(m))
	}
	return curateElapsed(d)
}

// curateCut keeps the first n characters of a reply, on one line, marked when
// cut.
func curateCut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "..."
	}
	return s
}

// collectionCurateItems is the row the collection page offers: one, naming
// what runs, or none when nobody curates the collection.
func (T *OrchestrateApp) collectionCurateItems(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	desc := "Carries out the plan above once: the articles you listed that are not in yet, links agents suggested, then the search terms. Every page it finds is checked before it goes in."
	return []map[string]any{{
		"item":    "Run now",
		"desc":    desc,
		"history": strings.TrimSpace(planSize(c) + " " + refreshLine(c.Owner, c.ID)),
	}}, nil
}

// collectionCurateStart starts a curation, or answers that one is running.
func (T *OrchestrateApp) collectionCurateStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	_, started, err := T.startCuration(c, call.User, "Running the plan")
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if !started {
		return map[string]any{"running": true, "message": "Already running for this collection"}, nil
	}
	return map[string]any{"running": true, "message": "Running the plan for " + c.Name}, nil
}

// startCuration runs the collection's plan for user, under label: Run now's
// run and a scheduled refresh's. started is false when a run is already
// going (the run returned is that one); an error says why it cannot run.
func (T *OrchestrateApp) startCuration(c Collection, user, label string) (run *curateRun, started bool, err error) {
	if !hasPlan(c) {
		return nil, false, fmt.Errorf("this collection has no plan yet: Draft a plan under Curation, or list articles for it")
	}
	if len(c.SearchTerms) > 0 && !planSearchReady() {
		return nil, false, fmt.Errorf("web search is not configured for this deployment, and the plan's search terms need it")
	}
	before, _ := collectionStats(VectorDB, c.ID)
	count := func() int { n, _ := collectionStats(VectorDB, c.ID); return n }
	udb := UserDB(T.DB, user)
	run, started = curations.start(c.ID, c, label, before, count, func(ctx context.Context, note func(string)) (string, error) {
		Log("[orchestrate.curate] collection=%s user=%s: plan run, %s", c.ID, user, strings.ToLower(label))
		return T.runPlan(ctx, udb, c, note)
	})
	return run, started, nil
}

// collectionCurateProgress is the run's progress or outcome; {} with neither
// when nothing has run lately.
func (T *OrchestrateApp) collectionCurateProgress(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(c.ID); ok {
		return run.status(), nil
	}
	return map[string]any{}, nil
}

// collectionCurateStop stops a running curation.
func (T *OrchestrateApp) collectionCurateStop(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(c.ID); ok {
		run.stop()
	}
	return map[string]any{"ok": true}, nil
}
