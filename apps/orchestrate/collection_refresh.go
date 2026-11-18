package orchestrate

// Refresh on a schedule: a curated collection's curator run every week or
// month, looking for what is new (docs/design/knowledge-curation.md, step 5).
//
// The schedule is a task on gohort's scheduler, not a field of the
// collection: one task per refreshed collection, holding the collection, how
// often, and what the last refresh did. Each firing schedules the next before
// it runs (so a restart mid-run does not end the chain), runs the plan the
// way Run now does, and then writes what it did onto that next task, which
// is what the page reads back. Never is no task. A collection that is gone, or
// hand curated, ends the chain at its next firing.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// collectionRefreshKind is the scheduler task a refresh is.
const collectionRefreshKind = "orchestrate.collection_refresh"

// refreshEvery are the intervals offered, by the value the page sends.
var refreshEvery = map[string]time.Duration{
	"week":  7 * 24 * time.Hour,
	"month": 30 * 24 * time.Hour,
}

// refreshTask is a refresh's scheduled task: whose collection, how often, and
// how the last one went.
type refreshTask struct {
	Collection string `json:"collection"`
	Owner      string `json:"owner"`
	Every      string `json:"every"`
	LastRan    string `json:"last_ran,omitempty"` // RFC3339
	LastNote   string `json:"last_note,omitempty"`
}

// refreshScheduled is the collection's scheduled refresh, with its task's id
// and when it is due, if there is one.
func refreshScheduled(owner, collection string) (task refreshTask, id string, due time.Time, ok bool) {
	for _, t := range ListScheduledTasks(collectionRefreshKind) {
		var p refreshTask
		if json.Unmarshal(t.Payload, &p) != nil || p.Collection != collection || p.Owner != owner {
			continue
		}
		at, _ := time.Parse(time.RFC3339, t.RunAt)
		return p, t.ID, at, true
	}
	return refreshTask{}, "", time.Time{}, false
}

// setRefresh makes the collection's schedule every ("week", "month", or
// "never"/"" for none), keeping what the last refresh did. A schedule already
// at that interval is left as it is, so saving the form does not push the
// next refresh back.
func setRefresh(owner, collection, every string) error {
	every = strings.TrimSpace(every)
	old, id, _, ok := refreshScheduled(owner, collection)
	if every == "" || every == "never" {
		if ok {
			UnscheduleTask(id)
			collectionRefreshChanged(owner, collection)
		}
		return nil
	}
	interval, known := refreshEvery[every]
	if !known {
		return fmt.Errorf("refresh every week, every month, or never")
	}
	if ok && old.Every == every {
		return nil
	}
	if ok {
		UnscheduleTask(id)
	}
	old.Collection, old.Owner, old.Every = collection, owner, every
	_, err := ScheduleTask(collectionRefreshKind, old, time.Now().Add(interval))
	collectionRefreshChanged(owner, collection)
	return err
}

// collectionRefreshChanged records a change of an app's collection's
// refresh, part of its blueprint (app_owned.go), as the person's own change
// to the app: the schedule is not on the collection, so its save does not
// see it.
func collectionRefreshChanged(owner, collection string) {
	if orchestrateBaseDB == nil {
		return
	}
	if c, ok := loadCollection(UserDB(orchestrateBaseDB, owner), owner, collection); ok && c.OwningApp != "" {
		AppOwnedChanged(owner, c.OwningApp, "collection", c.Name, "")
	}
}

// refreshLine is what the page says about the schedule: when the next one is
// due and what the last one did, or "" when there is none.
func refreshLine(owner, collection string) string {
	p, _, due, ok := refreshScheduled(owner, collection)
	if !ok {
		return ""
	}
	line := "Refreshes every " + p.Every + ": next on " + due.Local().Format("Mon 2 Jan") + "."
	if p.LastRan != "" {
		if at, err := time.Parse(time.RFC3339, p.LastRan); err == nil {
			line += " Last on " + at.Local().Format("Mon 2 Jan")
			if n := strings.TrimSpace(p.LastNote); n != "" {
				line += ": " + n
			} else {
				line += "."
			}
		}
	}
	return line
}

// handleCollectionRefresh is a refresh firing.
func (T *OrchestrateApp) handleCollectionRefresh(ctx context.Context, raw json.RawMessage) {
	var p refreshTask
	if json.Unmarshal(raw, &p) != nil || p.Collection == "" || p.Owner == "" {
		return
	}
	interval, ok := refreshEvery[p.Every]
	if !ok {
		return
	}
	c, found := loadCollection(UserDB(T.DB, p.Owner), p.Owner, p.Collection)
	if !found || c.Owner != p.Owner {
		Log("[orchestrate.refresh] collection=%s owner=%s: gone, schedule ended", p.Collection, p.Owner)
		return
	}
	// The next one first, so a restart during this run does not end the chain.
	nextID, err := ScheduleTask(collectionRefreshKind, p, time.Now().Add(interval))
	if err != nil {
		Log("[orchestrate.refresh] collection=%s: could not schedule the next refresh: %v", c.ID, err)
	}
	note := T.runRefresh(ctx, c, p)
	p.LastRan, p.LastNote = time.Now().UTC().Format(time.RFC3339), curateCut(note, 300)
	if nextID != "" {
		UpdateScheduledTaskPayload(nextID, p)
	}
}

// runRefresh runs the curator for a refresh and waits for it, returning what
// to note: its outcome, or why it did not run.
func (T *OrchestrateApp) runRefresh(ctx context.Context, c Collection, p refreshTask) string {
	run, started, err := T.startCuration(c, p.Owner, "Refreshing")
	if err != nil {
		return "did not run: " + err.Error()
	}
	if !started {
		return "skipped: a curation was already running"
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return "did not finish: the server was stopping"
	}
	out, _ := run.status()["outcome"].(string)
	return out
}
