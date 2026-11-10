package monitor

// The Monitor's one table: everything running, then what finished recently.
//
// Two feeds meet here. Agent runs come from orchestrate's runs ledger, which
// knows a run's sub-agents, whether it can be stopped, its trace, and how it
// ended. Everything else (apps, pipelines, queued work, peers borrowing this
// machine) comes from the live feed, which knows only what is running NOW: an
// app's entry is gone the moment it finishes. So the Monitor watches the
// feed and keeps a finished app entry for a while itself, to list it among
// the recent ones with when it ended.
//
// An agent run is in both feeds. Its live entry points at its own row here
// (/monitor?run=), so that entry is left out for a run the ledger already
// lists. Someone else's run is not in this viewer's ledger, so it stays, as
// the live feed shows it: masked, with no trace and no Stop.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

const (
	// keepFor is how long finished app work stays listed (the runs ledger's
	// default for agent runs, so the two kinds leave together), and keepMost
	// how many such entries at most.
	keepFor  = 30 * time.Minute
	keepMost = 20
	// watchEvery is how often the Monitor reads the live feed for work that
	// finished while no one was looking at the page.
	watchEvery = 5 * time.Second
	// runLinkPrefix is how an agent run's live entry points here.
	runLinkPrefix = "/monitor?run="
)

// monitorRow is one row of the table. _id is the run or live entry's id;
// _run marks the viewer's own agent run, which has a trace; href is where an
// app's row opens.
type monitorRow struct {
	Name        string `json:"name"`
	Via         string `json:"via"`
	Status      string `json:"status"`
	Doing       string `json:"doing,omitempty"`
	ID          string `json:"_id"`
	Href        string `json:"href,omitempty"`
	Run         bool   `json:"_run,omitempty"`
	Running     bool   `json:"_running,omitempty"`
	Cancellable bool   `json:"_cancellable,omitempty"`
	ended       time.Time
}

// seenWork is one live entry the Monitor has seen: since it first saw it,
// and when it was gone (zero while it runs).
type seenWork struct {
	entry LiveEntry
	since time.Time
	ended time.Time
}

// workLog is the live feed as the Monitor has watched it: what is running,
// with when it was first seen, and the app work that finished recently.
type workLog struct {
	mu    sync.Mutex
	live  map[string]*seenWork
	ended []*seenWork // newest first
}

// work is the live feed as this process has watched it: one Monitor per
// process, and the app's value-receiver methods copy the app.
var (
	work      workLog
	watchOnce sync.Once
)

// isRunEntry reports whether a live entry is an agent run's, which points at
// its own row on this page.
func isRunEntry(e LiveEntry) bool { return strings.HasPrefix(e.URL, runLinkPrefix) }

// worthKeeping reports whether finished work is listed among the recent: it
// was running, not waiting in a queue, when last seen, and it has a page to
// open, where its result is. Work with nowhere to go (a peer borrowing the
// model, a turn with no page) says nothing once it has ended. An agent run is
// never kept here: the runs ledger keeps it, with how it ended.
func worthKeeping(e LiveEntry) bool {
	return !e.Queued && !isRunEntry(e) && (e.URL != "" || e.Path != "")
}

// observe takes the live feed as it is at now: new work is noted, and work
// that is gone is kept as finished when it is worth keeping.
func (l *workLog) observe(entries []LiveEntry, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live == nil {
		l.live = map[string]*seenWork{}
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.ID] = true
		if w, ok := l.live[e.ID]; ok {
			w.entry = e
			continue
		}
		l.live[e.ID] = &seenWork{entry: e, since: now}
		l.dropEnded(e.ID) // the same work running again is not finished
	}
	for id, w := range l.live {
		if present[id] {
			continue
		}
		delete(l.live, id)
		if worthKeeping(w.entry) {
			w.ended = now
			l.ended = append([]*seenWork{w}, l.ended...)
		}
	}
	kept := l.ended[:0]
	for _, w := range l.ended {
		if now.Sub(w.ended) < keepFor && len(kept) < keepMost {
			kept = append(kept, w)
		}
	}
	l.ended = kept
}

func (l *workLog) dropEnded(id string) {
	for i, w := range l.ended {
		if w.entry.ID == id {
			l.ended = append(l.ended[:i], l.ended[i+1:]...)
			return
		}
	}
}

// since is when the Monitor first saw running work, or now when it has not.
func (l *workLog) since(id string, now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.live[id]; ok {
		return w.since
	}
	return now
}

// finished is a copy of the finished work kept, newest first.
func (l *workLog) finished() []seenWork {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]seenWork, len(l.ended))
	for i, w := range l.ended {
		out[i] = *w
	}
	return out
}

// watch reads the live feed every watchEvery, so work that finishes while no
// one has the page open is still listed when someone opens it.
func (T *MonitorApp) watch() {
	watchOnce.Do(func() {
		go func() {
			for range time.Tick(watchEvery) {
				work.observe(AllLiveSessions(), time.Now())
			}
		}()
	})
}

// agentActivity is the viewer's agent runs from orchestrate's ledger, or none
// when orchestrate is not running here.
func agentActivity(user, run string) []orchestrate.ActivityRow {
	o, ok := FindAgentAs[*orchestrate.OrchestrateApp]("orchestrate")
	if !ok || o == nil || !AppEnabledHere(o.WebPath()) {
		return nil
	}
	return o.Activity(user, run)
}

// handleActivity serves the table: GET /monitor/api/activity[?run=<id>].
func (T *MonitorApp) handleActivity(c *jsonapi.Call) ([]monitorRow, error) {
	run := strings.TrimSpace(c.R.URL.Query().Get("run"))
	agents := agentActivity(c.User, run)
	if run != "" {
		// One run and what it started: the ledger is the whole answer.
		return T.rows(c.R, agents, nil, nil, time.Now()), nil
	}
	now := time.Now()
	entries := AllLiveSessions()
	work.observe(entries, now)
	return T.rows(c.R, agents, entries, work.finished(), now), nil
}

// rows merges the viewer's agent runs with the live feed and the finished
// app work kept: running first (the viewer's agent runs in their tree, then
// the rest of the feed in its own order), then everything finished, newest
// first.
func (T *MonitorApp) rows(r *http.Request, agents []orchestrate.ActivityRow, entries []LiveEntry, ended []seenWork, now time.Time) []monitorRow {
	running, done := []monitorRow{}, []monitorRow{}
	listed := map[string]bool{}
	for _, a := range agents {
		listed[a.ID] = true
		row := monitorRow{Name: a.Agent, Via: a.Surface, Status: a.Activity, Doing: a.Brief, ID: a.ID,
			Run: true, Running: a.Running, Cancellable: a.Cancellable, ended: a.Ended}
		if a.Running {
			running = append(running, row)
		} else {
			done = append(done, row)
		}
	}
	var feed []LiveEntry
	for _, e := range entries {
		if isRunEntry(e) && listed[e.ID] {
			continue // the viewer's own run: its ledger row above says more
		}
		feed = append(feed, e)
	}
	sort.SliceStable(feed, func(i, j int) bool {
		if feed[i].Order != feed[j].Order {
			return feed[i].Order < feed[j].Order
		}
		return feed[i].App < feed[j].App
	})
	for i, e := range ViewLive(r, feed) {
		running = append(running, liveRow(e, feed[i].URL, work.since(e.ID, now), now))
	}
	var gone []LiveEntry
	for _, w := range ended {
		gone = append(gone, w.entry)
	}
	for i, e := range ViewLive(r, gone) {
		done = append(done, endedRow(e, ended[i], now))
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].ended.After(done[j].ended) })
	return append(running, done...)
}

// liveRow is running work from the live feed, as the viewer may see it (e);
// rawURL is the entry's own link, before the viewer's view of it.
func liveRow(e LiveEntry, rawURL string, since, now time.Time) monitorRow {
	row := monitorRow{Name: e.App, Via: "app", Doing: e.Label, ID: e.ID, Href: e.Href, Running: !e.Queued}
	if row.Name == "" {
		row.Name = "Task"
	}
	if strings.HasPrefix(rawURL, runLinkPrefix) {
		// Someone else's agent run: listed, not opened; its trace is theirs.
		row.Via, row.Href = "agent", ""
	}
	if e.Background {
		row.Via += " - background"
	}
	if e.Queued {
		row.Status = joinStatus("queued", e.Status)
	} else {
		row.Status = joinStatus("● running: "+elapsed(now.Sub(since)), e.Status)
	}
	return row
}

// endedRow is finished app work. An app's entry says only that it is gone,
// not how it went, so the row says "ended" and its page says the rest.
func endedRow(e LiveEntry, w seenWork, now time.Time) monitorRow {
	return monitorRow{Name: e.App, Via: "app", Doing: e.Label, ID: e.ID, Href: e.Href, ended: w.ended,
		Status: "ended " + elapsed(now.Sub(w.ended)) + " ago: took about " + elapsed(w.ended.Sub(w.since))}
}

func joinStatus(head, status string) string {
	if status = strings.TrimSpace(status); status == "" {
		return head
	}
	return head + ", " + status
}

// elapsed is a duration the way the agent rows say it: 42s, 3m10s, 1h04m.
func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
