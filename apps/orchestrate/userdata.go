package orchestrate

// A deleted account's agents, pipelines, machines and schedules.
//
// Moving gives somebody else what the person BUILT: the agents (sub-agents
// with them, so the graph stays whole), pipelines and machines, and the
// people they were shared with keep them under the new owner. What the
// person ACCUMULATED does not move: conversations, memory, learned knowledge,
// and the approvals they gave a tool. Those were theirs, and the new owner
// decides their own.
//
// Schedules never move. A schedule runs as a person, so one that kept firing
// under a new owner would act in their name without their having chosen it.
// Purge stops every one before anything else, including after a move, since
// the account's deletion runs purge to clear what is left: a deleted account's
// standing agents were otherwise still firing.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/peershare"
)

type orchUserData struct{ app *OrchestrateApp }

func (h *orchUserData) AppName() string { return "agents" }

// ownedAgents are the agents uid built: not the in-code seeds or their
// per-user shadows, and not an app agent's settings, which belong to the app
// that declared it.
func ownedAgents(udb Database, uid string) []AgentRecord {
	var out []AgentRecord
	for _, a := range listAgents(udb, uid) {
		if a.Owner != uid || strings.HasPrefix(a.ID, "seed-") || isAppAgent(a.ID) {
			continue
		}
		if _, seed := seedAgentByID(a.ID); seed {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (h *orchUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{AppName: "agents", Counts: map[string]int{}, Actions: []string{"reassign", "purge"}}
	udb := UserDB(h.app.DB, uid)
	if udb == nil {
		return sum
	}
	for _, a := range ownedAgents(udb, uid) {
		sum.Counts["agents"]++
		if len(a.AllowedUsers) > 0 || a.Everyone || a.MCPExposed {
			sum.Counts["shared"]++
		}
	}
	for _, d := range ListPipelineDefs(udb, uid) {
		sum.Counts["pipelines"]++
		if len(d.AllowedUsers) > 0 || d.Published {
			sum.Counts["shared"]++
		}
	}
	for _, m := range ListMachineDefs(udb, uid) {
		sum.Counts["machines"]++
		if len(m.AllowedUsers) > 0 || m.Published {
			sum.Counts["shared"]++
		}
	}
	sum.Counts["schedules"] = len(ListStandingAgents(RootDB, uid)) + len(ListEventMonitors(RootDB, uid)) +
		len(listAgentRecurringTasks(uid, ""))
	return sum
}

// Reassign moves what was built. Records are written straight into the new
// owner's store: they are already valid, and going through the save path
// would file kept versions and check sub-agents against parents that have
// not arrived yet.
func (h *orchUserData) Reassign(from, to string) error {
	src, dst := UserDB(h.app.DB, from), UserDB(h.app.DB, to)
	if src == nil || dst == nil || from == to {
		return nil
	}
	for _, a := range ownedAgents(src, from) {
		a.Owner = to
		dst.Set(agentsTable, a.ID, a)
		src.Unset(agentsTable, a.ID)
		if RootDB != nil {
			peershare.MoveOwner(RootDB, SharedAgentsTable, from, to, a.ID)
		}
		// What it learned stays with the person who taught it.
		dropAgentSideData(src, from, a.ID)
	}
	for _, d := range ListPipelineDefs(src, from) {
		d.Owner = to
		dst.Set(PipelineDefsTable, d.ID, d)
		src.Unset(PipelineDefsTable, d.ID)
		syncPipelineShareIndex(d)
	}
	for _, m := range ListMachineDefs(src, from) {
		m.Owner = to
		dst.Set(MachineDefsTable, m.ID, m)
		src.Unset(MachineDefsTable, m.ID)
		syncMachineShareIndex(m)
	}
	return nil
}

func (h *orchUserData) Anonymize(string) error { return ErrUserDataActionNotSupported }

// Purge stops every schedule the account had, then deletes what it built
// through each kind's own delete, so whoever depended on it is told.
func (h *orchUserData) Purge(uid string) error {
	stopUserSchedules(uid)
	udb := UserDB(h.app.DB, uid)
	if udb == nil {
		return nil
	}
	for _, a := range ownedAgents(udb, uid) {
		deleteAgent(udb, a.ID, uid) // a parent takes its sub-agents; theirs then find nothing
	}
	for _, d := range ListPipelineDefs(udb, uid) {
		DeletePipelineDef(udb, d.ID)
	}
	for _, m := range ListMachineDefs(udb, uid) {
		DeleteMachineDef(udb, m.ID)
	}
	return nil
}

// stopUserSchedules cancels and removes everything that runs on its own as
// uid: standing agents, monitors, triggers (an app's self-updating actions
// among them), recurring tasks, and channels. Each delete cancels its live
// timer, which is the part that matters: a record removed with its timer left
// running fires for an account that no longer exists.
func stopUserSchedules(uid string) int {
	if RootDB == nil || uid == "" {
		return 0
	}
	n := 0
	for _, sa := range ListStandingAgents(RootDB, uid) {
		DeleteStandingAgent(RootDB, uid, sa.Name)
		n++
	}
	for _, m := range ListEventMonitors(RootDB, uid) {
		DeleteEventMonitor(RootDB, uid, m.Name)
		n++
	}
	for _, t := range ListScheduledTriggers(RootDB, uid) {
		DeleteScheduledTrigger(RootDB, uid, t.Name)
		n++
	}
	for _, r := range listAgentRecurringTasks(uid, "") {
		UnscheduleTask(r.TaskID)
		n++
	}
	for _, ch := range ListChannels(RootDB, uid) {
		DeleteChannel(RootDB, uid, ch.ID)
		n++
	}
	if n > 0 {
		Log("[orchestrate] stopped %d schedule(s) and channel(s) for deleted account %q", n, uid)
	}
	return n
}
