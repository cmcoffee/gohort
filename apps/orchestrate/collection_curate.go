package orchestrate

// Curate now: the collection's curator, run on the collection's behalf.
//
// A curator (Collection.CuratorAgent) has always been able to keep its
// collection: it gets the collection tool for it on every run. What it lacked
// was anybody asking. It did nothing until someone thought to say so in a chat
// or gave it a schedule, so "Auto-curated" would have been a promise with no
// run behind it. This is that run, started from the collection page:
//
//	GET  /api/collections/{id}/curate           the row to press, empty when by hand
//	POST /api/collections/{id}/curate           start (one run per collection)
//	GET  /api/collections/{id}/curate/progress  {"progress"} while it runs, {"outcome"} after
//	POST /api/collections/{id}/curate/stop      stop it
//
// Whoever is curator does the work, the Knowledge app's own curating agent or
// one of the person's agents alike, with the tools it already has. The run is
// on a context of its own, so closing the page does not stop it, and a page
// opened mid-run rejoins it. The caps are Auto-fill's: its document limit is
// what the curator is told to stay within, and its timeout ends the run.

import (
	"context"
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

// curateRunAgent runs the curator. A variable so a test can stand in for the
// agent run.
var curateRunAgent = func(T *OrchestrateApp, ctx context.Context, user, agentID, message string) (string, error) {
	return T.RunAgentSync(ctx, user, user, agentID, message)
}

// curateRun is one collection's curation, running or recently ended.
type curateRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
	before  int

	mu            sync.Mutex
	docs          int
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

// start runs work for collection id unless a run for it is already going, in
// which case that one is returned and started is false.
func (r *curateRegistry) start(id string, before int, count func() int, work func(ctx context.Context) (string, error)) (run *curateRun, started bool) {
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
	run = &curateRun{cancel: cancel, done: make(chan struct{}), started: time.Now(), before: before, docs: before}
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
		reply, err := work(ctx)
		close(stop)
		<-counted
		n := count()
		run.mu.Lock()
		run.docs, run.reply, run.err, run.ended = n, reply, err, time.Now()
		if ctx.Err() == context.DeadlineExceeded {
			run.err = fmt.Errorf("it ran past the %s limit for one run", curateLimit(autofillTimeout()))
		}
		run.mu.Unlock()
	}()
	return run, true
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
	added := fmt.Sprintf("%d document%s added", run.added(), plural(run.added()))
	if run.ended.IsZero() {
		return map[string]any{"progress": "Curating - " + added + " - " + curateElapsed(time.Since(run.started))}
	}
	took := curateElapsed(run.ended.Sub(run.started))
	switch {
	case run.stopRequested:
		return map[string]any{"outcome": "Stopped after " + took + ", " + added + "."}
	case run.err != nil:
		return map[string]any{"outcome": "Failed after " + took + ", " + added + ": " + run.err.Error()}
	}
	out := "Done in " + took + ", " + added + "."
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

// curateBrief is what the curator is asked to do. Built from the collection's
// own description, which is what Auto-fill and research read too, so what the
// person said the collection is for steers what is found.
func curateBrief(c Collection, docs, maxDocs int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Look after the knowledge collection %q now. You are in charge of it, and the collection tool works on it.\n\n", c.Name)
	if d := strings.TrimSpace(c.Description); d != "" {
		b.WriteString("What it is for, in its owner's words:\n" + d + "\n\n")
	} else {
		b.WriteString("It has no description, so go by its name.\n\n")
	}
	if r := strings.TrimSpace(c.FilterRules); r != "" {
		b.WriteString("Its owner's rules for what goes in it:\n" + r + "\n\n")
	}
	fmt.Fprintf(&b, "It holds %d document%s now.\n\n", docs, plural(docs))
	b.WriteString("1. List what is in it first, so you do not add what is already there.\n")
	b.WriteString("2. Work through any suggestions waiting from agents that read it: check each against its source, then add what holds up and dismiss what does not.\n")
	fmt.Fprintf(&b, "3. Find material that fits what it is for and is missing, and add the best of it by its address. Add no more than %d documents in this run. Prefer primary and authoritative sources over summaries of them, and current material over old.\n", maxDocs)
	b.WriteString("4. Do not remove anything. If a document is out of date, wrong, or does not belong, name it in your reply as a proposed removal and say why.\n\n")
	b.WriteString("Reply in a few sentences for the owner: what you added and why, and any removals you propose.")
	return b.String()
}

// collectionCurateItems is the row the collection page offers: one, naming
// what runs, or none when nobody curates the collection.
func (T *OrchestrateApp) collectionCurateItems(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.CuratorAgent) == "" {
		return []map[string]any{}, nil
	}
	desc := "The curator looks for material that fits what this collection is for, adds what is missing, and proposes removals for you to confirm."
	if a, ok := findAgentByNameOrID(UserDB(T.DB, call.User), call.User, c.CuratorAgent); ok && !hiddenAppAgent(a.ID) {
		desc = chFirst(a.Name, c.CuratorAgent) + " looks for material that fits what this collection is for, adds what is missing, and proposes removals for you to confirm."
	}
	return []map[string]any{{
		"item":    "Curate now",
		"desc":    desc,
		"history": fmt.Sprintf("Adds up to %d documents a run, within %s.", autofillMaxDocs(), curateLimit(autofillTimeout())),
	}}, nil
}

// collectionCurateStart starts a curation, or answers that one is running.
func (T *OrchestrateApp) collectionCurateStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	curator := strings.TrimSpace(c.CuratorAgent)
	if curator == "" {
		return nil, jsonapi.BadRequest("nobody curates this collection: choose Auto-curated or one of your agents under Curation first")
	}
	user := call.User
	a, ok := findAgentByNameOrID(UserDB(T.DB, user), user, curator)
	if !ok {
		return nil, jsonapi.BadRequest("the curator chosen for this collection is not an agent of yours any more: choose another under Curation")
	}
	if hiddenAppAgent(a.ID) && !WebSearchAvailable() {
		return nil, jsonapi.BadRequest("web search is not configured for this deployment, and auto-curation finds material with it")
	}
	before, _ := collectionStats(VectorDB, c.ID)
	brief := curateBrief(c, before, autofillMaxDocs())
	count := func() int { n, _ := collectionStats(VectorDB, c.ID); return n }
	_, started := curations.start(c.ID, before, count, func(ctx context.Context) (string, error) {
		Log("[orchestrate.curate] collection=%s curator=%s user=%s: started", c.ID, a.ID, user)
		reply, err := curateRunAgent(T, ctx, user, a.ID, brief)
		if err != nil {
			Log("[orchestrate.curate] collection=%s: ended: %v", c.ID, err)
		} else {
			Log("[orchestrate.curate] collection=%s: done", c.ID)
		}
		return reply, err
	})
	if !started {
		return map[string]any{"running": true, "message": "Already curating this collection"}, nil
	}
	return map[string]any{"running": true, "message": "Curating " + c.Name}, nil
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
