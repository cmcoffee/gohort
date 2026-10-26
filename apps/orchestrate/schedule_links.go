package orchestrate

// Link: schedules that run together, one after another, at one time.
//
// A link group is two or more schedules (scheduled agents and recurring tasks,
// from any of the owner's agents) that share ONE time: the group's own, which
// can be random. When it comes, the members run in order, each starting when
// the one before it has finished. There is no parent and no child; the group
// owns the time and the members are slots 1, 2, 3.
//
// A member's own time is set aside while it is linked, not deleted: a
// scheduled agent keeps its Cron / interval and simply is not armed, and a
// recurring task keeps its pattern and is parked on a dormant tick. Unlinking
// puts it back exactly as it was.
//
// One member failing does not stop the rest. Each still reports its own run
// into its own thread; the group records how each went, for its card.
//
// Event monitors are not linkable: they fire on events, not a clock.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/timing"
)

const (
	linkGroupsTable = "schedule_link_groups"
	linkFireKind    = "orchestrate.link_group"
	schedKindLink   = "link"
)

// linkMember is one slot in a group. ID is a scheduled agent's name, or a
// recurring task's UID (its scheduler id changes on every fire).
type linkMember struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (m linkMember) ref() string { return m.Kind + ":" + m.ID }

// linkMemberRun is how one member's part of the last group run went.
type linkMemberRun struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

type linkGroup struct {
	ID      string       `json:"id"`
	Owner   string       `json:"owner"`
	Members []linkMember `json:"members"`

	// The group's time, in the same fields and by the same rule as a
	// scheduled agent's (StandingAgent.NextRunFrom).
	Cron               string    `json:"cron,omitempty"`
	StartAt            time.Time `json:"start_at,omitzero"`
	IntervalSeconds    int       `json:"interval_seconds,omitempty"`
	MaxIntervalSeconds int       `json:"max_interval_seconds,omitempty"`
	HasWindow          bool      `json:"has_window,omitempty"`
	WindowFromMin      int       `json:"window_from_min,omitempty"`
	WindowToMin        int       `json:"window_to_min,omitempty"`
	// Timing is the shared form (core/timing); when set it is the whole answer
	// and the fields above are the older one.
	Timing timing.Spec `json:"timing,omitzero"`

	Paused  bool      `json:"paused,omitempty"`
	NextRun time.Time `json:"next_run,omitzero"`
	// Rounds counts the group's runs (every member once), for a Timing that
	// stops after a number of them; EndNote says why it ended when it did.
	Rounds      int    `json:"rounds,omitempty"`
	EndNote     string `json:"end_note,omitempty"`
	SchedulerID string `json:"scheduler_id,omitempty"`

	LastRunAt time.Time       `json:"last_run_at,omitzero"`
	LastRun   []linkMemberRun `json:"last_run,omitempty"`
	Created   time.Time       `json:"created"`
}

// timing is the group's time as a scheduled agent's, so the next fire and the
// label come from the one implementation.
func (g linkGroup) timing() StandingAgent {
	return StandingAgent{Name: "link " + g.ID, Owner: g.Owner, Cron: g.Cron, StartAt: g.StartAt,
		IntervalSeconds: g.IntervalSeconds, MaxIntervalSeconds: g.MaxIntervalSeconds,
		HasWindow: g.HasWindow, WindowFromMin: g.WindowFromMin, WindowToMin: g.WindowToMin, Timing: g.Timing}
}

func (g *linkGroup) setTiming(sa StandingAgent) {
	g.Cron, g.StartAt, g.IntervalSeconds, g.MaxIntervalSeconds = sa.Cron, sa.StartAt, sa.IntervalSeconds, sa.MaxIntervalSeconds
	g.HasWindow, g.WindowFromMin, g.WindowToMin = sa.HasWindow, sa.WindowFromMin, sa.WindowToMin
	g.Timing = sa.Timing
}

func linkKey(owner, id string) string { return owner + "\x00" + id }

// linkMu serializes changes to groups, so two edits cannot interleave a
// read-modify-write of the same group.
var linkMu sync.Mutex

func getLinkGroup(owner, id string) (linkGroup, bool) {
	var g linkGroup
	if RootDB == nil || owner == "" || id == "" {
		return g, false
	}
	ok := RootDB.Get(linkGroupsTable, linkKey(owner, id), &g)
	return g, ok
}

func saveLinkGroup(g linkGroup) {
	if RootDB != nil {
		RootDB.Set(linkGroupsTable, linkKey(g.Owner, g.ID), g)
	}
}

func listLinkGroups(owner string) []linkGroup {
	var out []linkGroup
	if RootDB == nil || owner == "" {
		return out
	}
	for _, k := range RootDB.Keys(linkGroupsTable) {
		if !strings.HasPrefix(k, owner+"\x00") {
			continue
		}
		var g linkGroup
		if RootDB.Get(linkGroupsTable, k, &g) {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// arm schedules the group's next fire, or clears it while paused.
func (g *linkGroup) arm() error {
	if g.SchedulerID != "" {
		UnscheduleTask(g.SchedulerID)
		g.SchedulerID = ""
	}
	g.NextRun = time.Time{}
	if g.Paused {
		saveLinkGroup(*g)
		return nil
	}
	// Its stops: a number of rounds, or a date the next round would be past.
	// Ended rather than deleted, like a schedule that met its goal.
	spec, hasSpec := g.timing().EffectiveTiming()
	if hasSpec && spec.MaxRuns > 0 && g.Rounds >= spec.MaxRuns {
		g.Paused, g.EndNote = true, fmt.Sprintf("ran %d rounds, as it was set to", g.Rounds)
		saveLinkGroup(*g)
		return nil
	}
	next, err := g.timing().NextRunFrom(time.Now().In(UserLocation(g.Owner)))
	if err != nil {
		return err
	}
	if hasSpec && !spec.StopAt.IsZero() && !next.Before(spec.StopAt) {
		g.Paused, g.EndNote = true, "reached its end date, "+spec.StopAt.Add(-time.Second).Format("Jan 2")
		saveLinkGroup(*g)
		return nil
	}
	id, err := ScheduleTask(linkFireKind, linkFirePayload{Owner: g.Owner, ID: g.ID}, next)
	if err != nil {
		return err
	}
	g.SchedulerID, g.NextRun = id, next
	saveLinkGroup(*g)
	return nil
}

type linkFirePayload struct {
	Owner string `json:"owner"`
	ID    string `json:"id"`
}

// --- members -----------------------------------------------------------------

// linkTarget is a member resolved against its live record.
type linkTarget struct {
	m      linkMember
	name   string
	agent  string // the agent it belongs to, for labels and scoping
	taskID string // recurring: the current scheduler id
	sa     StandingAgent
	p      orchUpdatePayload
}

func resolveLinkMember(owner string, m linkMember) (linkTarget, bool) {
	t := linkTarget{m: m}
	switch m.Kind {
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, owner, m.ID)
		if !ok {
			return t, false
		}
		t.sa, t.name, t.agent = sa, sa.Name, firstNonEmptyStr(sa.AgentID, sa.ReportAgentID)
		return t, true
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(owner, "") {
			if recurringTaskUID(rt.Payload) == m.ID {
				t.p, t.taskID, t.name, t.agent = rt.Payload, rt.TaskID, recurringName(rt.Payload), rt.Payload.AgentID
				return t, true
			}
		}
	}
	return t, false
}

// linkMemberFromRef reads a row's "<kind>:<id>" into a member. A recurring
// row is addressed by its scheduler id, which is turned into its UID here.
func linkMemberFromRef(owner, ref string) (linkMember, bool) {
	kind, id, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || id == "" {
		return linkMember{}, false
	}
	switch kind {
	case schedKindStanding:
		if _, found := GetStandingAgent(RootDB, owner, id); found {
			return linkMember{Kind: kind, ID: id}, true
		}
	case schedKindRecurring:
		for _, rt := range listAgentRecurringTasks(owner, "") {
			if rt.TaskID == id || recurringTaskUID(rt.Payload) == id {
				return linkMember{Kind: kind, ID: recurringTaskUID(rt.Payload)}, true
			}
		}
	}
	return linkMember{}, false
}

// groupOf is the link group a resolved member is in, or "".
func (t linkTarget) groupOf() string {
	if t.m.Kind == schedKindStanding {
		return t.sa.LinkGroup
	}
	return t.p.LinkGroup
}

// setAside puts a member on the group's time: its own is kept, not armed.
func (t linkTarget) setAside(owner, groupID string) error {
	switch t.m.Kind {
	case schedKindStanding:
		sa := t.sa
		sa.LinkGroup = groupID
		return ScheduleStandingAgent(RootDB, sa) // linked: unarms and saves
	case schedKindRecurring:
		UnscheduleTask(t.taskID)
		p := t.p
		p.LinkGroup = groupID
		next, _ := computeNextFire(&p, time.Now())
		_, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next)
		return err
	}
	return nil
}

// restore takes a member off the group's time and back onto its own.
func (t linkTarget) restore(owner string) {
	switch t.m.Kind {
	case schedKindStanding:
		sa := t.sa
		sa.LinkGroup = ""
		if sa.Paused || sa.Broken {
			SaveStandingAgent(RootDB, sa)
			return
		}
		if err := ScheduleStandingAgent(RootDB, sa); err != nil {
			Log("[orchestrate/link] %s back on its own time failed: %v", sa.Name, err)
		}
	case schedKindRecurring:
		UnscheduleTask(t.taskID)
		p := t.p
		p.LinkGroup = ""
		next, err := computeNextFire(&p, time.Now().In(UserLocation(owner)))
		if err != nil || p.Paused {
			next = time.Now().Add(brokenDormantReArm) // a paused one stays held
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			Log("[orchestrate/link] %s back on its own time failed: %v", recurringName(p), err)
		}
	}
}

// pruneLinkGroup drops members that are gone or have left the group, and
// dissolves a group left with fewer than two: the one remaining goes back on
// its own time. Reports whether the group still exists. Caller holds linkMu.
func pruneLinkGroup(g *linkGroup) bool {
	var kept []linkMember
	var live []linkTarget
	for _, m := range g.Members {
		t, ok := resolveLinkMember(g.Owner, m)
		if !ok || t.groupOf() != g.ID {
			continue
		}
		kept = append(kept, m)
		live = append(live, t)
	}
	if len(kept) >= 2 {
		if len(kept) != len(g.Members) {
			g.Members = kept
			saveLinkGroup(*g)
		}
		return true
	}
	for _, t := range live {
		t.restore(g.Owner)
	}
	deleteLinkGroup(*g)
	return false
}

func deleteLinkGroup(g linkGroup) {
	if g.SchedulerID != "" {
		UnscheduleTask(g.SchedulerID)
	}
	linkGroupNotes(g.Owner, g.ID).drop() // the shared note ends with the group
	if RootDB != nil {
		RootDB.Unset(linkGroupsTable, linkKey(g.Owner, g.ID))
	}
}

// --- the run -----------------------------------------------------------------

type linkFireKey struct{}

// withLinkFire marks a run as started by its group's time, which is the one
// thing a linked recurring task's own fire path runs for.
func withLinkFire(ctx context.Context) context.Context {
	return context.WithValue(ctx, linkFireKey{}, true)
}

func isLinkFire(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(linkFireKey{}).(bool)
	return v
}

// runLinkGroup runs every member in order. Each starts when the one before it
// has finished; one failing does not stop the rest. Only a scheduled round
// counts toward a "stop after N rounds"; Run now is a one-off.
func runLinkGroup(ctx context.Context, owner, id string, scheduled bool) {
	linkMu.Lock()
	g, ok := getLinkGroup(owner, id)
	if ok {
		ok = pruneLinkGroup(&g)
	}
	linkMu.Unlock()
	if !ok || g.Paused {
		return
	}
	ctx = withLinkFire(ctx)
	results := make([]linkMemberRun, 0, len(g.Members))
	for _, m := range g.Members {
		results = append(results, runLinkMember(ctx, owner, m))
	}
	linkMu.Lock()
	defer linkMu.Unlock()
	if cur, ok := getLinkGroup(owner, id); ok {
		cur.LastRunAt, cur.LastRun = time.Now(), results
		if scheduled {
			cur.Rounds++
		}
		saveLinkGroup(cur)
	}
}

func runLinkMember(ctx context.Context, owner string, m linkMember) linkMemberRun {
	t, ok := resolveLinkMember(owner, m)
	if !ok {
		return linkMemberRun{Name: m.ID, Status: "skipped", Note: "it no longer exists"}
	}
	r := linkMemberRun{Name: t.name}
	switch m.Kind {
	case schedKindStanding:
		switch {
		case t.sa.Paused:
			r.Status, r.Note = "skipped", "paused"
			return r
		case t.sa.Broken:
			r.Status, r.Note = "skipped", "needs relink"
			return r
		}
		if StandingAgentDependencyError != nil {
			if reason := StandingAgentDependencyError(t.sa); reason != "" {
				MarkStandingAgentBroken(RootDB, owner, t.sa.Name, reason)
				r.Status, r.Note = "skipped", reason
				return r
			}
		}
		rec, err := RunStandingAgentNow(ctx, RootDB, owner, t.sa.Name, "link")
		if err != nil {
			r.Status, r.Note = "failed", err.Error()
			return r
		}
		r.Status = string(rec.Status)
	case schedKindRecurring:
		if t.p.Broken {
			r.Status, r.Note = "skipped", "needs relink"
			return r
		}
		if t.p.Paused {
			r.Status, r.Note = "skipped", "paused"
			return r
		}
		// The scheduler consumes an entry as it fires it; this fire is the
		// group's, so the member's own dormant entry goes the same way, and
		// the fire arms its successor (dormant again, while linked).
		UnscheduleTask(t.taskID)
		if err := fireOrchestrateUpdate(ctx, t.p, true); err != nil {
			r.Status, r.Note = "failed", err.Error()
			return r
		}
		r.Status = "ran"
	}
	return r
}

// registerLinkGroups wires the group's fire and a reconciler that re-arms a
// group whose timer was lost (a restart mid-run, an entry dropped). Called once
// at startup with the other schedulers.
func registerLinkGroups() {
	RegisterScheduleHandler(linkFireKind, func(ctx context.Context, raw json.RawMessage) {
		var p linkFirePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			Log("[orchestrate/link] bad payload: %v", err)
			return
		}
		// Re-arm whatever happens, as a scheduled agent does: the group is a
		// self-rescheduling chain, and a run that panicked must not end it.
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate/link] group %s/%s PANICKED: %v, re-arming anyway", p.Owner, p.ID, r)
			}
			linkMu.Lock()
			defer linkMu.Unlock()
			if g, ok := getLinkGroup(p.Owner, p.ID); ok && !g.Paused {
				if err := g.arm(); err != nil {
					Log("[orchestrate/link] re-arm failed for group %s/%s: %v", p.Owner, p.ID, err)
				}
			}
		}()
		runLinkGroup(ctx, p.Owner, p.ID, true)
	})
	RegisterReconciler("link_rearm", func(ctx context.Context) error {
		rearmStrandedLinkGroups()
		return nil
	})
}

func rearmStrandedLinkGroups() {
	if RootDB == nil {
		return
	}
	live := map[string]bool{}
	for _, t := range ListScheduledTasks(linkFireKind) {
		var p linkFirePayload
		if json.Unmarshal(t.Payload, &p) == nil {
			live[linkKey(p.Owner, p.ID)] = true
		}
	}
	linkMu.Lock()
	defer linkMu.Unlock()
	for _, k := range RootDB.Keys(linkGroupsTable) {
		var g linkGroup
		if !RootDB.Get(linkGroupsTable, k, &g) || g.Paused || live[k] {
			continue
		}
		if err := g.arm(); err != nil {
			Log("[orchestrate/link] re-arm of stranded group %s/%s failed: %v", g.Owner, g.ID, err)
		}
	}
}

// --- linking -----------------------------------------------------------------

// linkTimingOf is the time a new group starts with: the schedule the link was
// made from. A recurring task's pattern maps where it can; one that cannot
// (a set number of random fires a day) starts the group hourly, to be edited.
func linkTimingOf(t linkTarget) StandingAgent {
	if t.m.Kind == schedKindStanding {
		return t.sa
	}
	var sa StandingAgent
	switch p := t.p; {
	case p.Pattern == RecurringDaily && len(p.AtMinutes) == 1:
		sa.Cron = "daily " + fmtHHMM(p.AtMinutes[0])
	case p.Pattern == RecurringRandom && p.TimesPerDay <= 0 && p.MaxGapSeconds > p.MinGapSeconds && p.MinGapSeconds > 0:
		sa.IntervalSeconds, sa.MaxIntervalSeconds = p.MinGapSeconds, p.MaxGapSeconds
	case p.IntervalSeconds > 0:
		sa.IntervalSeconds = p.IntervalSeconds
	default:
		sa.IntervalSeconds = 3600
	}
	if t.p.HasWindow && sa.Cron == "" {
		sa.HasWindow, sa.WindowFromMin, sa.WindowToMin = true, t.p.WindowFromMin, t.p.WindowToMin
	}
	return sa
}

// linkSchedules links from (a row, not yet linked) with value: another
// unlinked schedule ("<kind>:<id>"), which starts a new group on from's time,
// or an existing group ("link:<id>"), which from joins at the end.
func linkSchedules(owner, fromRef, value string) error {
	linkMu.Lock()
	defer linkMu.Unlock()
	fm, ok := linkMemberFromRef(owner, fromRef)
	if !ok {
		return Error("that schedule was not found")
	}
	from, ok := resolveLinkMember(owner, fm)
	if !ok {
		return Error("that schedule was not found")
	}
	if from.groupOf() != "" {
		return Error("it is already linked: unlink it first")
	}
	if kind, id, _ := strings.Cut(value, ":"); kind == schedKindLink {
		g, ok := getLinkGroup(owner, id)
		if !ok {
			return Error("that link group no longer exists")
		}
		if err := from.setAside(owner, g.ID); err != nil {
			return err
		}
		g.Members = append(g.Members, fm)
		saveLinkGroup(g)
		return nil
	}
	om, ok := linkMemberFromRef(owner, value)
	if !ok {
		return Error("the schedule to link with was not found")
	}
	if om == fm {
		return Error("a schedule cannot be linked with itself")
	}
	other, ok := resolveLinkMember(owner, om)
	if !ok {
		return Error("the schedule to link with was not found")
	}
	if other.groupOf() != "" {
		return Error("that schedule is already linked: join its group instead")
	}
	g := linkGroup{ID: UUIDv4()[:12], Owner: owner, Members: []linkMember{fm, om}, Created: time.Now()}
	g.setTiming(linkTimingOf(from))
	if err := g.arm(); err != nil {
		return fmt.Errorf("the group's time would not arm: %w", err)
	}
	for _, t := range []linkTarget{from, other} {
		if err := t.setAside(owner, g.ID); err != nil {
			return err
		}
	}
	Log("[orchestrate/link] %s linked %s with %s as group %s", owner, fm.ref(), om.ref(), g.ID)
	return nil
}

// unlinkSchedule takes one member out of its group and back onto its own time.
func unlinkSchedule(owner, ref string) error {
	linkMu.Lock()
	defer linkMu.Unlock()
	m, ok := linkMemberFromRef(owner, ref)
	if !ok {
		return Error("that schedule was not found")
	}
	t, _ := resolveLinkMember(owner, m)
	g, ok := getLinkGroup(owner, t.groupOf())
	if !ok {
		return Error("it is not linked")
	}
	var kept []linkMember
	for _, x := range g.Members {
		if x != m {
			kept = append(kept, x)
		}
	}
	g.Members = kept
	saveLinkGroup(g)
	t.restore(owner)
	pruneLinkGroup(&g)
	return nil
}

// moveInLinkGroup moves a member one slot earlier (-1) or later (+1).
func moveInLinkGroup(owner, ref string, by int) error {
	linkMu.Lock()
	defer linkMu.Unlock()
	m, ok := linkMemberFromRef(owner, ref)
	if !ok {
		return Error("that schedule was not found")
	}
	t, _ := resolveLinkMember(owner, m)
	g, ok := getLinkGroup(owner, t.groupOf())
	if !ok {
		return Error("it is not linked")
	}
	for i, x := range g.Members {
		if x != m {
			continue
		}
		j := i + by
		if j < 0 || j >= len(g.Members) {
			return nil
		}
		g.Members[i], g.Members[j] = g.Members[j], g.Members[i]
		saveLinkGroup(g)
		return nil
	}
	return Error("it is not in its group")
}

// dissolveLinkGroups removes every group the owner has, without restoring the
// members' own times: it is part of stopping a deleted account's schedules,
// where every member is being deleted anyway.
func dissolveLinkGroups(owner string) {
	linkMu.Lock()
	defer linkMu.Unlock()
	for _, g := range listLinkGroups(owner) {
		deleteLinkGroup(g)
	}
}

// --- the Scheduler -----------------------------------------------------------

// linkOption is one choice in the Link picker.
type linkOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"`
}

// linkOptions is what the schedule at rowRef can be linked with: the groups it
// can join, its own agent's schedules, then everyone else's by agent name,
// labelled "(Agent) task". Monitors, parked schedules and schedules already in
// a group are not offered, and a row already linked is offered nothing (it
// unlinks first). fallback is the agent to treat as local when the row does
// not say.
func linkOptions(owner string, names map[string]string, rowRef, fallback string) []linkOption {
	agentName := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return firstNonEmptyStr(id, "no agent")
	}
	local, self, selfGroup := fallback, "", ""
	if m, ok := linkMemberFromRef(owner, rowRef); ok {
		if t, ok := resolveLinkMember(owner, m); ok {
			local, self, selfGroup = t.agent, m.ref(), t.groupOf()
		}
	}
	var out []linkOption
	if selfGroup != "" {
		return out // linked: Unlink first (see linkSchedules)
	}
	for _, g := range listLinkGroups(owner) {
		out = append(out, linkOption{Value: schedKindLink + ":" + g.ID, Label: "Join: " + linkGroupTitle(owner, g, names), Group: "Link groups"})
	}
	type choice struct{ ref, name, agent string }
	var mine, others []choice
	add := func(c choice) {
		if c.ref == self {
			return
		}
		if local != "" && c.agent == local {
			mine = append(mine, c)
		} else {
			others = append(others, c)
		}
	}
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if sa.LinkGroup == "" && !sa.Broken {
			add(choice{schedKindStanding + ":" + sa.Name, sa.Name, firstNonEmptyStr(sa.AgentID, sa.ReportAgentID)})
		}
	}
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if rt.Payload.LinkGroup == "" && !rt.Payload.Broken && recurringTaskUID(rt.Payload) != "" {
			add(choice{schedKindRecurring + ":" + recurringTaskUID(rt.Payload), recurringName(rt.Payload), rt.Payload.AgentID})
		}
	}
	lower := strings.ToLower
	sort.SliceStable(mine, func(i, j int) bool { return lower(mine[i].name) < lower(mine[j].name) })
	sort.SliceStable(others, func(i, j int) bool {
		ai, aj := lower(agentName(others[i].agent)), lower(agentName(others[j].agent))
		if ai != aj {
			return ai < aj
		}
		return lower(others[i].name) < lower(others[j].name)
	})
	for _, c := range mine {
		out = append(out, linkOption{Value: c.ref, Label: c.name, Group: agentName(local)})
	}
	for _, c := range others {
		out = append(out, linkOption{Value: c.ref, Label: "(" + agentName(c.agent) + ") " + c.name, Group: "Other agents"})
	}
	return out
}

// linkGroupTitle names a group by its members in order.
// linkGroupNamed finds one of the owner's groups by the id a listing shows,
// as an agent might write it: bare, quoted, or as "link group <id>".
func linkGroupNamed(owner, name string) (linkGroup, bool) {
	id := strings.TrimSpace(name)
	for _, p := range []string{"link group ", "link:", "group "} {
		if len(id) > len(p) && strings.EqualFold(id[:len(p)], p) {
			id = strings.TrimSpace(id[len(p):])
		}
	}
	id = strings.Trim(id, "\"' ")
	if id == "" {
		return linkGroup{}, false
	}
	return getLinkGroup(owner, id)
}

// linkGroupAgents is every agent with a schedule in the group.
func linkGroupAgents(owner string, g linkGroup) []string {
	var out []string
	for _, m := range g.Members {
		if t, ok := resolveLinkMember(owner, m); ok && t.agent != "" {
			out = append(out, t.agent)
		}
	}
	return out
}

// linkGroupsListing tells an agent what its schedule listing could not: which
// schedules run together, on whose time, and that the group is paused or
// resumed as one, by its id. Without it an agent asked to hold a group's
// schedules paused them one by one, or offered to and could not.
func linkGroupsListing(owner string) string {
	groups := listLinkGroups(owner)
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("LINK GROUPS (members run one after another on the group's time, not their own. Pause or resume a whole group with schedule(action=\"pause\" / \"resume\", name=\"<its id>\"); pausing one member only skips that one when the group runs)\n")
	for _, g := range groups {
		state := "active"
		if g.Paused {
			state = "paused"
		}
		fmt.Fprintf(&b, "- %q: %s; %s, %s\n", g.ID, linkGroupTitle(owner, g, nil), StandingScheduleLabel(g.timing()), state)
	}
	return strings.TrimSpace(b.String())
}

// linkMemberNote is what a pause or resume of one member says about its
// group, so the agent knows the rest of the group goes on.
func linkMemberNote(owner, groupID string, paused bool) string {
	g, ok := getLinkGroup(owner, groupID)
	if !ok {
		return ""
	}
	if paused {
		return fmt.Sprintf(" It is in link group %q, which still runs its other members on the group's time; pause the group (name=%q) to hold them all.", g.ID, g.ID)
	}
	if g.Paused {
		return fmt.Sprintf(" It runs with link group %q, on the group's time, and the group itself is paused: nothing runs until the group is resumed (name=%q).", g.ID, g.ID)
	}
	next := ""
	if !g.NextRun.IsZero() {
		next = ", next " + g.NextRun.In(UserLocation(owner)).Format("Mon Jan 2 3:04 PM")
	}
	return fmt.Sprintf(" It runs with link group %q, on the group's time%s.", g.ID, next)
}

func linkGroupTitle(owner string, g linkGroup, names map[string]string) string {
	var parts []string
	for _, m := range g.Members {
		if t, ok := resolveLinkMember(owner, m); ok {
			parts = append(parts, t.name)
		}
	}
	return strings.Join(parts, " → ")
}

// appendLinkGroupRows adds each group as a header row followed by its members
// in order, all sharing _group so the page draws them in one bordered box.
// Returns the members it placed, so their own sections leave them out.
//
// In one agent's view a group shows when any member is that agent's, and the
// card lists only that agent's members: the header says how many run from
// other agents, and each member's step number is its place in the whole order.
// The all-agents view lists every member, labelled "(Agent) name" as every
// row there is (agentRowLabel).
func appendLinkGroupRows(out []map[string]any, user string, udb Database, agentID string, ix *standingRunIndex) ([]map[string]any, map[string]bool) {
	placed := map[string]bool{}
	linkMu.Lock()
	var groups []linkGroup
	for _, g := range listLinkGroups(user) {
		if pruneLinkGroup(&g) {
			groups = append(groups, g)
		}
	}
	linkMu.Unlock()
	if len(groups) == 0 {
		return out, placed
	}
	names := agentNames(udb, user)
	standingRows := map[string]consoleAgentRow{}
	for _, r := range consoleAgentRows(user, udb, "", ix) {
		standingRows[r.ID] = r
	}
	recurringRows := map[string]consoleRecurringRow{}
	for _, r := range consoleRecurringRows(user, "") {
		recurringRows[r.ID] = r
	}
	loc := UserLocation(user)
	for _, g := range groups {
		var targets []linkTarget
		mine := agentID == ""
		for _, m := range g.Members {
			t, ok := resolveLinkMember(user, m)
			if !ok {
				continue
			}
			targets = append(targets, t)
			if t.agent == agentID {
				mine = true
			}
		}
		if !mine {
			continue
		}
		elsewhere := 0
		if agentID != "" {
			for _, t := range targets {
				if t.agent != agentID {
					elsewhere++
				}
			}
		}
		state := "active"
		if g.Paused {
			state = "paused"
			if g.EndNote != "" {
				state = "✓ done: " + g.EndNote
			}
		}
		next := ""
		if !g.NextRun.IsZero() {
			next = g.NextRun.In(loc).Format("Mon Jan 2 3:04 PM")
		}
		head := map[string]any{
			"group":       linkHeadTitle(len(targets), elsewhere),
			"schedule":    StandingScheduleLabel(g.timing()),
			"next_run":    next,
			"state":       state,
			"_section":    "Linked",
			"_group":      g.ID,
			"_group_head": true,
			"_id":         g.ID,
			"_kind":       schedKindLink,
			"_link_head":  true,
			"_notes":      true, // the group's shared note
			"_link_pause": !g.Paused,
			"_link_go":    g.Paused,
			"_paused":     g.Paused,
		}
		if last := linkLastRunLabel(g, loc); last != "" {
			head["last_run"] = last
		}
		out = append(out, head)
		for i, t := range targets {
			if agentID != "" && t.agent != agentID {
				continue // another agent's: counted in the header, listed on its own Scheduler
			}
			var m map[string]any
			switch t.m.Kind {
			case schedKindStanding:
				m = schedulerRow(standingRows[t.sa.Name], "Linked", schedKindStanding)
			case schedKindRecurring:
				m = schedulerRow(recurringRows[t.taskID], "Linked", schedKindRecurring)
			}
			if m == nil {
				continue
			}
			placed[t.m.ref()] = true
			if _, ok := m["name"]; ok && agentID == "" {
				m["name"] = agentRowLabel(names, t.agent, t.name)
			}
			m["schedule"] = fmt.Sprintf("step %d of %d", i+1, len(targets))
			m["next_run"] = ""
			delete(m, "cadence")
			m["_group"] = g.ID
			m["_linked"] = true
			// One meaning per button: a linked row is moved by Unlink then
			// Link, and grown from the other schedule's row ("Join").
			m["_link"] = false
			// A member's runs use the group's note, on the header; its own is
			// set aside while it is linked.
			m["_notes"] = false
			m["_link_up"] = i > 0
			m["_link_down"] = i < len(targets)-1
			// The time is the group's: its own Pause is still offered (the
			// group then skips it), its own timing edit is not.
			m["_pause_standing"], m["_resume_standing"] = false, false
			out = append(out, m)
		}
	}
	return out, placed
}

// linkHeadTitle is the group card's title.
func linkHeadTitle(total, elsewhere int) string {
	title := fmt.Sprintf("Linked: %d schedules, one after another", total)
	switch {
	case elsewhere == 1:
		title += ", 1 of them another agent's"
	case elsewhere > 1:
		title += fmt.Sprintf(", %d of them other agents'", elsewhere)
	}
	return title
}

// linkLastRunLabel says how the last group run went, member by member.
func linkLastRunLabel(g linkGroup, loc *time.Location) string {
	if g.LastRunAt.IsZero() || len(g.LastRun) == 0 {
		return ""
	}
	var parts []string
	for _, r := range g.LastRun {
		s := r.Name + " " + r.Status
		if r.Note != "" {
			s += " (" + r.Note + ")"
		}
		parts = append(parts, s)
	}
	return "last run " + g.LastRunAt.In(loc).Format("Jan 2 3:04 PM") + ": " + strings.Join(parts, ", ")
}

// --- handlers ----------------------------------------------------------------

func (T *OrchestrateApp) handleConsoleLinkOptions(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	q := r.URL.Query()
	return linkOptions(user, agentNames(udb, user), strings.TrimSpace(q.Get("ref")), strings.TrimSpace(q.Get("agent"))), nil
}

// handleConsoleLink serves the row verbs: POST /api/console/links/<verb>?id=
// (the last path segment is the verb, so a mount prefix does not matter).
// link (value = what to link with), unlink, up, down take a row's "<kind>:<id>";
// pause, resume, run take a group id.
func (T *OrchestrateApp) handleConsoleLink(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	verb := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	var err error
	switch verb {
	case "link":
		err = linkSchedules(user, id, strings.TrimSpace(r.URL.Query().Get("value")))
	case "unlink":
		err = unlinkSchedule(user, id)
	case "up":
		err = moveInLinkGroup(user, id, -1)
	case "down":
		err = moveInLinkGroup(user, id, 1)
	case "pause", "resume":
		err = setLinkGroupPaused(user, id, verb == "pause")
	case "run":
		if _, ok := getLinkGroup(user, id); !ok {
			err = Error("that link group no longer exists")
			break
		}
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					Log("[orchestrate/link] run-now panicked for group %s: %v", id, rec)
				}
			}()
			runLinkGroup(context.Background(), user, id, false)
		}()
	case "update":
		err = T.updateLinkGroupTime(user, id, r)
	default:
		return nil, errNotFound
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

func setLinkGroupPaused(owner, id string, paused bool) error {
	linkMu.Lock()
	defer linkMu.Unlock()
	g, ok := getLinkGroup(owner, id)
	if !ok {
		return Error("that link group no longer exists")
	}
	if !paused && g.EndNote != "" {
		g.Rounds, g.EndNote = 0, "" // resuming an ended group gives it a fresh count
	}
	g.Paused = paused
	return g.arm()
}

// handleConsoleLinkGet is the group's time for the editor, in the fields the
// scheduled-agent editor's timing half reads.
func (T *OrchestrateApp) handleConsoleLinkGet(c *jsonapi.Call) (any, error) {
	user, id := c.User, strings.TrimSpace(c.R.URL.Query().Get("id"))
	g, ok := getLinkGroup(user, id)
	if !ok {
		return nil, jsonapi.NotFound("no such link group")
	}
	var members []string
	for i, m := range g.Members {
		if t, ok := resolveLinkMember(user, m); ok {
			members = append(members, fmt.Sprintf("%d. %s", i+1, t.name))
		}
	}
	return map[string]any{
		"cron":                 g.Cron,
		"interval_minutes":     g.IntervalSeconds / 60,
		"max_interval_minutes": g.MaxIntervalSeconds / 60,
		"active_from":          standingWindowEdge(g.timing(), g.WindowFromMin),
		"active_to":            standingWindowEdge(g.timing(), g.WindowToMin),
		"schedule_label":       StandingScheduleLabel(g.timing()),
		"timing":               standingTimingOut(g.timing()),
		// A group stops on a date or after a number of rounds; its members
		// carry any goal.
		"until":   func() untilOut { u := standingUntilOut(g.timing(), false); u.Runs = g.Rounds; return u }(),
		"members": members,
	}, nil
}

// updateLinkGroupTime sets the group's time, through the same validation a
// scheduled agent's editor posts through. A time that will not arm is refused
// and the old one kept.
func (T *OrchestrateApp) updateLinkGroupTime(owner, id string, r *http.Request) error {
	var body standingUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return Error("bad body: " + err.Error())
	}
	body.Mission = nil
	if body.Until != nil {
		body.Until.Goal = nil // a group has no goal of its own
	}
	linkMu.Lock()
	defer linkMu.Unlock()
	g, ok := getLinkGroup(owner, id)
	if !ok {
		return Error("that link group no longer exists")
	}
	before := g
	sa := g.timing()
	if err := applyStandingUpdate(&sa, body); err != nil {
		return err
	}
	g.setTiming(sa)
	if err := g.arm(); err != nil {
		_ = before.arm()
		return err
	}
	return nil
}

// linkRowRef is a Scheduler row's member reference ("<kind>:<name or UID>"),
// for telling which rows a group has already placed.
func linkRowRef(m map[string]any) string {
	kind, _ := m["_kind"].(string)
	id, _ := m["_id"].(string)
	switch kind {
	case schedKindStanding:
		return kind + ":" + id
	case schedKindRecurring:
		if uid, _ := m["_uid"].(string); uid != "" {
			return kind + ":" + uid
		}
	}
	return ""
}
