// Self-updating custom apps: a scheduled AppAction fires its script unattended on
// a timer and upserts the records it returns, so a dashboard/tracker stays fresh
// while nobody has the page open. This rides entirely on the unified trigger
// engine (core/trigger.go) as target_kind "customapp_action" — NOT a parallel
// scheduler — so a scheduled action shows up in the same admin trigger surface as
// every other standing trigger, survives restart via the persisted trigger store,
// and reuses the exact run-and-upsert path a button click takes
// (runActionAndPersist).
//
// Lifecycle:
//   - reconcile-on-save: SaveAppSpec fires a hook (RegisterAppSpecSavedHook) that
//     diffs an app's actions and creates/updates/removes one trigger per scheduled
//     action. DeleteAppSpec removes them all.
//   - idle pause: a page view stamps last-viewed; a fire whose app hasn't been
//     viewed within MaxIdleDays pauses its own trigger. The next view re-arms it.
//   - review gate: a Disabled app registers no triggers, so an imported app runs
//     nothing unattended before its owner enables it.
package customapps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	targetKindCustomAppAction = "customapp_action"
	lastViewTable             = "customapp_lastview" // owner:slug -> RFC3339 view time
)

// registerScheduling wires the self-update machinery once, from Routes(). It
// registers the trigger dispatcher for our target kind, starts the (idempotent)
// trigger scheduler, and installs the spec-lifecycle hooks that keep triggers in
// sync with each app's scheduled actions.
func (T *CustomApps) registerScheduling() {
	RegisterTriggerAction(targetKindCustomAppAction, func(ctx context.Context, t ScheduledTrigger, _ string) {
		T.dispatchScheduledAction(ctx, t)
	})
	StartTriggerScheduler()
	RegisterAppSpecSavedHook(func(s AppSpec) { T.reconcileAppSchedules(s) })
	RegisterAppSpecDeletedHook(func(owner, slug string) { removeAppTriggers(owner, slug) })
}

// schedTriggerName is the stable per-action trigger name (unique per owner).
func schedTriggerName(slug, action string) string {
	return "customapp:" + slug + ":" + action
}

// reconcileAppSchedules brings the owner's standing triggers into agreement with
// this spec's scheduled actions: upsert a trigger for each scheduled action of an
// enabled app, and remove any trigger for an action that lost its schedule, was
// deleted, or whose app is disabled. Idempotent — safe to call on every save.
func (T *CustomApps) reconcileAppSchedules(spec AppSpec) {
	want := map[string]AppAction{}
	if !spec.Disabled {
		for _, a := range spec.Actions {
			if a.Schedule.Scheduled() {
				want[schedTriggerName(spec.Slug, a.Name)] = a
			}
		}
	}
	// Drop triggers for this app that are no longer wanted.
	for _, tr := range ListScheduledTriggers(RootDB, spec.Owner) {
		if tr.TargetKind != targetKindCustomAppAction || tr.TargetID != spec.Slug {
			continue
		}
		if _, keep := want[tr.Name]; !keep {
			DeleteScheduledTrigger(RootDB, spec.Owner, tr.Name)
		}
	}
	// Upsert the wanted ones.
	for name, a := range want {
		upsertAppTrigger(spec.Owner, spec.Slug, name, a)
	}
}

// removeAppTriggers deletes every scheduled-action trigger for one app (used when
// the app itself is deleted).
func removeAppTriggers(owner, slug string) {
	for _, tr := range ListScheduledTriggers(RootDB, owner) {
		if tr.TargetKind == targetKindCustomAppAction && tr.TargetID == slug {
			DeleteScheduledTrigger(RootDB, owner, tr.Name)
		}
	}
}

// upsertAppTrigger creates or updates one action's trigger, preserving its live
// scheduling state (SchedulerID/Created/Paused) across edits and only
// (re)scheduling when the cadence changed or nothing is scheduled yet.
func upsertAppTrigger(owner, slug, name string, a AppAction) {
	secs := a.Schedule.IntervalSeconds
	if secs > 0 && secs < MinAppScheduleSeconds {
		secs = MinAppScheduleSeconds
	}
	tr := ScheduledTrigger{
		Name:            name,
		Owner:           owner,
		Gate:            GateAlways,
		Action:          ActionNotify, // unused by our dispatcher; a sane default for the ledger
		IntervalSeconds: secs,
		Cron:            a.Schedule.Cron,
		TargetKind:      targetKindCustomAppAction,
		TargetID:        slug,
		TargetMeta:      a.Name,
		Created:         time.Now(),
	}
	prev, ok := GetScheduledTrigger(RootDB, owner, name)
	cadenceSame := ok && prev.IntervalSeconds == tr.IntervalSeconds && prev.Cron == tr.Cron
	if ok {
		// Preserve live state so a no-cadence-change save doesn't reset the timer.
		tr.SchedulerID = prev.SchedulerID
		tr.Created = prev.Created
		tr.Paused = prev.Paused
		tr.NextRun = prev.NextRun
		tr.LastFired = prev.LastFired
		tr.RepeatCount = prev.RepeatCount
	}
	SaveScheduledTrigger(RootDB, tr)
	if !cadenceSame || prev.NextRun.IsZero() {
		if err := ScheduleTrigger(RootDB, tr); err != nil {
			Log("[customapps] schedule %q failed: %v", name, err)
		}
	}
}

// dispatchScheduledAction runs one scheduled action's script in the owner's
// sandbox and upserts its records — the unattended twin of a button click. It
// self-heals (deletes its own trigger if the app/action/schedule vanished) and
// self-pauses when the app has gone unviewed past MaxIdleDays.
func (T *CustomApps) dispatchScheduledAction(_ context.Context, t ScheduledTrigger) {
	owner, slug, actName := t.Owner, t.TargetID, t.TargetMeta
	spec, ok := LoadAppSpec(owner, slug)
	if !ok || spec.Disabled {
		DeleteScheduledTrigger(RootDB, owner, t.Name)
		return
	}
	var act *AppAction
	for i := range spec.Actions {
		if spec.Actions[i].Name == actName {
			act = &spec.Actions[i]
			break
		}
	}
	if act == nil || !act.Schedule.Scheduled() {
		DeleteScheduledTrigger(RootDB, owner, t.Name)
		return
	}
	// Idle pause: stop firing for an app nobody looks at. A page view re-arms it.
	if d := act.Schedule.MaxIdleDays; d > 0 {
		if last := appLastViewed(owner, slug); !last.IsZero() &&
			time.Since(last) > time.Duration(d)*24*time.Hour {
			pauseTrigger(owner, t.Name)
			Log("[customapps] %s/%s idle > %dd — pausing self-update", slug, actName, d)
			return
		}
	}

	db := T.recordBase(spec, owner)
	records := gatherRecords(db, recTable(slug))
	msg, saved, err := runActionAndPersist(owner, db, db, spec, *act, map[string]any{"records": records})
	if err != nil {
		Log("[customapps] scheduled action %q/%q failed: %v", slug, actName, err)
		return
	}
	Log("[customapps] self-update %s/%s: %s (%d saved)", slug, actName, msg, saved)
}

// appScheduleStatus summarizes an app's self-update state for the index badge:
// whether it has any scheduled action, whether every one is currently paused,
// and the soonest next run across the active ones.
func appScheduleStatus(owner, slug string) (has, allPaused bool, next time.Time) {
	active := 0
	for _, tr := range ListScheduledTriggers(RootDB, owner) {
		if tr.TargetKind != targetKindCustomAppAction || tr.TargetID != slug {
			continue
		}
		has = true
		if tr.Paused {
			continue
		}
		active++
		if !tr.NextRun.IsZero() && (next.IsZero() || tr.NextRun.Before(next)) {
			next = tr.NextRun
		}
	}
	return has, has && active == 0, next
}

// setAppSchedulesPaused pauses or resumes every scheduled action of one app and
// returns how many triggers changed. Resuming reschedules the next fire.
func setAppSchedulesPaused(owner, slug string, paused bool) int {
	n := 0
	for _, tr := range ListScheduledTriggers(RootDB, owner) {
		if tr.TargetKind != targetKindCustomAppAction || tr.TargetID != slug || tr.Paused == paused {
			continue
		}
		tr.Paused = paused
		SaveScheduledTrigger(RootDB, tr)
		if !paused {
			if err := ScheduleTrigger(RootDB, tr); err != nil {
				Log("[customapps] resume %q failed: %v", tr.Name, err)
			}
		}
		n++
	}
	return n
}

// humanizeNext renders a next-run time as a coarse relative string for the badge.
func humanizeNext(t time.Time) string {
	d := time.Until(t)
	switch {
	case d < 0:
		return "due"
	case d < time.Minute:
		return "in <1m"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}

// pauseTrigger marks a trigger paused without deleting it; a later page view
// clears the flag and reschedules via reconcileAppSchedules.
func pauseTrigger(owner, name string) {
	if tr, ok := GetScheduledTrigger(RootDB, owner, name); ok && !tr.Paused {
		tr.Paused = true
		SaveScheduledTrigger(RootDB, tr)
	}
}

// touchAppView stamps an app's last-viewed time and re-arms any of its schedules
// that had self-paused from idleness. Called on every page render.
func (T *CustomApps) touchAppView(spec AppSpec) {
	if RootDB == nil {
		return
	}
	RootDB.Set(lastViewTable, spec.Owner+":"+spec.Slug, time.Now().UTC().Format(time.RFC3339))
	// Re-arm any schedule this app self-paused from idleness: clear the flag and
	// reschedule the next fire. Only writes when a paused trigger actually exists.
	for _, tr := range ListScheduledTriggers(RootDB, spec.Owner) {
		if tr.TargetKind != targetKindCustomAppAction || tr.TargetID != spec.Slug || !tr.Paused {
			continue
		}
		tr.Paused = false
		SaveScheduledTrigger(RootDB, tr)
		if err := ScheduleTrigger(RootDB, tr); err != nil {
			Log("[customapps] re-arm %q failed: %v", tr.Name, err)
		}
	}
}

func appLastViewed(owner, slug string) time.Time {
	if RootDB == nil {
		return time.Time{}
	}
	var s string
	if !RootDB.Get(lastViewTable, owner+":"+slug, &s) || strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// gatherRecords reads a record table into the JSON string the action script
// expects as its `records` arg (mirrors handleAction's own gathering).
func gatherRecords(db Database, tbl string) string {
	out := []map[string]any{}
	for _, k := range db.Keys(tbl) {
		var rec map[string]any
		if db.Get(tbl, k, &rec) {
			out = append(out, rec)
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}
