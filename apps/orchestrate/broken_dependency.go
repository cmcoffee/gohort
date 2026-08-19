package orchestrate

import (
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// This file wires the fire-path dependency guards: before a monitor / standing
// agent fires, core consults these resolvers, and if a dependency it needs has
// been removed it marks the record broken (pause + unschedule + keep) instead of
// firing into the void or fail-looping. See core.EventMonitorDependencyError /
// core.StandingAgentDependencyError and MarkEventMonitorBroken.
//
// The resolvers are deliberately CONSERVATIVE: they report a dependency missing
// only when the existence check is authoritative (an agent absent from the
// owner's DB, a credential absent from the vault, a pipeline/tool absent from
// every place it could live). A fuzzy "maybe gone" always reads as healthy, so a
// working monitor is never falsely paused.

// agentExists reports whether an agent id resolves in the owner's per-user DB.
// Monitors/standing agents live in RootDB but reference their target agent by id
// in the owner's UserDB. An empty id means "deployment default channel agent",
// which always exists.
func agentExists(owner, id string) bool {
	if strings.TrimSpace(id) == "" {
		return true
	}
	_, ok := loadAgent(agentUserDB(RootDB, owner), id)
	return ok
}

// toolResolvable reports whether a named tool exists anywhere it could live: the
// static chat-tool registry, the shared deployment pool, or the owner's unified
// tool store (one lookup covers shared AND agent-scoped rows — any scope counts
// as "exists"). Mirrors the credential-tool scan (credential_tools.go) so a
// tool that genuinely still exists is never flagged missing.
func toolResolvable(owner, name string) bool {
	if _, ok := LookupChatTool(name); ok {
		return true
	}
	for _, pt := range LoadSharedPersistentTempTools(RootDB) {
		if pt.Tool.Name == name {
			return true
		}
	}
	if _, ok := UserToolByName(RootDB, owner, name); ok {
		return true
	}
	return false
}

// eventMonitorDependencyError returns a reason a monitor can no longer run — its
// wake/check agent deleted, a credential removed, or a tool/pipeline gone — or
// "" if healthy. Wired into core.EventMonitorDependencyError.
func eventMonitorDependencyError(m EventMonitor) string {
	// Legacy monitors created before the WakeAgent field carried an implicit
	// default: the framework Chat seed — now retired, so waking it would post
	// into a thread no user can open. A monitor with a channel target is
	// exempt (delivery goes into the channel, not an agent thread).
	if strings.TrimSpace(m.WakeAgent) == "" && strings.TrimSpace(m.WakeChannel) == "" {
		return "it has no wake agent — its old implicit default (the retired Chat seed) no longer runs; relink an agent to resume"
	}
	if !agentExists(m.Owner, m.WakeAgent) {
		return fmt.Sprintf("its wake agent was deleted (id %s)", m.WakeAgent)
	}
	if m.CheckAgent != "" && !agentExists(m.Owner, m.CheckAgent) {
		return fmt.Sprintf("its check agent was deleted (id %s)", m.CheckAgent)
	}
	// Source dependency only applies to a watch monitor (the kind that calls out
	// through a credential / tool / pipeline each interval).
	if m.Kind != EventKindWatch {
		return ""
	}
	switch {
	case strings.HasPrefix(m.ToolName, "call_") && m.ToolName != "call_no_auth":
		// url-source via a SecureAPI credential (bridge tool / rest_poll connector
		// both set ToolName = "call_"+cred).
		cred := strings.TrimPrefix(m.ToolName, "call_")
		if exists, _, _ := Secure().CredentialStatus(cred); !exists {
			return fmt.Sprintf("credential %q was removed", cred)
		}
	case m.SourceKind == "pipeline":
		base := strings.TrimPrefix(m.ToolName, "run_")
		if _, ok := LoadPipelineDef(agentUserDB(RootDB, m.Owner), m.Owner, base); !ok {
			return fmt.Sprintf("pipeline %q no longer exists", base)
		}
	case m.SourceKind == "tool":
		if !toolResolvable(m.Owner, m.ToolName) {
			return fmt.Sprintf("tool %q no longer exists", m.ToolName)
		}
	}
	return ""
}

// standingAgentDependencyError returns a reason a standing agent can no
// longer run — its target was deleted — or "" if healthy.
//
// The target is an agent OR a pipeline (StandingAgent.PipelineID), and
// the pipeline case was UNCHECKED. Not a false positive — agentExists
// answers true for an empty id, deliberately — so a schedule whose
// pipeline had been deleted read as healthy here and failed at fire
// time instead. Which is the quieter half of the same problem this
// package exists to solve: a dependency that is gone should be visible
// where somebody is looking at the schedule, not discovered at 3am.
func standingAgentDependencyError(sa StandingAgent) string {
	if sa.TargetsPipeline() {
		if !pipelineExists(sa.Owner, sa.PipelineID) {
			return "its target pipeline is out of reach — " + pipelineMissingReason(sa.Owner, sa.PipelineID)
		}
		return ""
	}
	if sa.TargetsMachine() {
		// Same posture as the pipeline case, plus one reason of its own: a
		// machine that has been switched back to conversing is still THERE,
		// and would still fail every fire. Broken-and-visible is the answer
		// to both, since both are repaired the same way.
		//
		// Reachable, not merely present — machineForUser, the same resolver
		// the fire path uses, so a machine somebody shared counts and one
		// whose share was withdrawn does not. Two answers to that question
		// would let the schedule list and the 3am run disagree.
		def, ok := machineForUser(sa.Owner, sa.MachineID)
		switch {
		case !ok:
			return "its target machine is out of reach — " + machineMissingReason(sa.Owner, sa.MachineID)
		case !def.Unattended:
			return "its target machine " + strconv.Quote(def.Name) + " converses rather than runs, so a schedule has nobody to answer the step that waits"
		}
		return ""
	}
	if !agentExists(sa.Owner, sa.AgentID) {
		return fmt.Sprintf("its target agent was deleted (id %s)", sa.AgentID)
	}
	return ""
}

// pipelineExists is agentExists' counterpart for a pipeline target.
// Reachable, not merely present: a pipeline shared with this user counts, and
// one whose share was withdrawn does not — which is the same answer the fire
// path gives, from the same helper, so the schedule list and the 3am run cannot
// disagree about whether a schedule is healthy.
func pipelineExists(owner, id string) bool {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(id) == "" {
		return false
	}
	_, ok := pipelineForUser(owner, id)
	return ok
}

// credentialDeleted marks every watch monitor that polls through a just-deleted
// credential broken, immediately — so it's paused + kept rather than left to
// fail at its next poll. Wired into core.CredentialDeletedHook. Both the bridge
// tool and the rest_poll connector set ToolName = "call_"+cred, so one prefix
// match covers both.
func credentialDeleted(cred string) {
	tool := "call_" + cred
	for _, m := range ListAllEventMonitors(RootDB) {
		if m.Kind == EventKindWatch && m.ToolName == tool {
			MarkEventMonitorBroken(RootDB, m.Owner, m.Name,
				fmt.Sprintf("credential %q was removed", cred))
		}
	}
}

// brokenStateLabel builds the visible "needs relink" state a console row shows
// when its record is broken. Shared by the monitor / standing / recurring list
// handlers so all three read identically.
func brokenStateLabel(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "⚠ needs relink"
	}
	return "⚠ needs relink — " + reason
}

// wireDependencyGuards installs the fire-path resolvers + the credential-delete
// hook. Called once at startup.
func wireDependencyGuards() {
	EventMonitorDependencyError = eventMonitorDependencyError
	StandingAgentDependencyError = standingAgentDependencyError
	CredentialDeletedHook = credentialDeleted
	PipelineDeletedHook = pipelineDeleted
	MachineDeletedHook = machineDeleted
	// Not dependency guards, but the same family and the same moment: save
	// hooks whose whole job is keeping an index true. Wired here so there is
	// one place to look for "what happens when one of these is written".
	PipelineSavedHook = syncPipelineShareIndex
	MachineSavedHook = syncMachineShareIndex
}

// scheduleOwners is every user whose schedules might point at a just-deleted
// pipeline: the owner first (the ordinary case, and the only one before
// sharing), then everybody else. Falls back to the owner alone when there is no
// user directory to walk, so a single-user deployment pays nothing.
func scheduleOwners(owner string) []string {
	out := []string{owner}
	if RootDB == nil {
		return out
	}
	for _, u := range AuthListUsers(RootDB) {
		if u.Username != "" && u.Username != owner {
			out = append(out, u.Username)
		}
	}
	return out
}

// pipelineScheduleGuard exists to be referenced from the agent-deletion
// sweep, so the two halves of the same rule are findable from each other.
const pipelineScheduleGuard = "see pipelineDeleted"

// pipelineDeleted marks every schedule that fires a just-deleted pipeline
// broken, immediately — so it is paused-and-kept rather than left to fail
// at its next fire. Wired into core.PipelineDeletedHook.
//
// The agent half of this rule has always existed (deleting an agent marks
// the schedules that run it). Without this one, deleting a pipeline left
// its schedules looking healthy until they fired, which is the same
// dependency going stale for longer — and "broken, kept, and relinkable"
// is the posture this package exists to hold.
func pipelineDeleted(owner, id, name string) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(id) == "" {
		return
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = id
	}
	// A deleted pipeline is shared with nobody. Dropped first: everything below
	// takes a moment, and a recipient resolving it in between would find the
	// index pointing at a record that is already gone.
	dropPipelineShareIndex(id)
	// Every user's schedules, not just the owner's. A shared pipeline can be
	// fired by somebody else's schedule, and deleting it leaves THEIR schedule
	// pointing at nothing — the case that is easiest to miss precisely because
	// the person who broke it never sees the thing they broke.
	for _, u := range scheduleOwners(owner) {
		for _, sa := range ListStandingAgents(RootDB, u) {
			if sa.PipelineID != id {
				continue
			}
			why := fmt.Sprintf("runs deleted pipeline %q", label)
			if u != owner {
				why = fmt.Sprintf("runs pipeline %q, which %s deleted", label, owner)
			}
			MarkStandingAgentBroken(RootDB, u, sa.Name, why)
		}
	}
	pruneDispatchTarget(owner, id)
}

// pruneDispatchTarget removes a deleted target's id from every one of this
// owner's agents' dispatch lists.
//
// A pipeline or a machine can be named on an agent's dispatch target list, so
// deleting one leaves the same dangling id that deleting an AGENT does — and
// the agent path already prunes those. Left in place, the entry is a grant to
// nothing that a later record could inherit by reusing the id, and it keeps an
// Only-mode list looking populated when it is not.
//
// Shared by both deletions rather than written twice: the list holds TARGETS,
// and a pruner that knew which kind it was pruning would be a pruner that has
// to be extended for the next kind.
func pruneDispatchTarget(owner, id string) {
	db := UserDB(RootDB, owner)
	if db == nil {
		return
	}
	for _, k := range db.Keys(agentsTable) {
		var other AgentRecord
		if !db.Get(agentsTable, k, &other) || other.Owner != owner || len(other.AllowedDispatchTargets) == 0 {
			continue
		}
		var kept []string
		changed := false
		for _, target := range other.AllowedDispatchTargets {
			if strings.EqualFold(strings.TrimSpace(target), id) {
				changed = true
				continue
			}
			kept = append(kept, target)
		}
		if changed {
			other.AllowedDispatchTargets = kept
			_, _ = saveAgent(db, other)
		}
	}
}

// machineDeleted is pipelineDeleted for the third kind of target: every
// schedule that fires a just-deleted machine is marked broken immediately, the
// share index is dropped, and the id stops being a grant on anybody's dispatch
// list. Wired into core.MachineDeletedHook.
//
// A machine's schedule is matched by NAME as well as id, unlike a pipeline's:
// StandingAgent.MachineID holds whatever the picker or the tool gave it, and
// findMachineByNameOrID has always resolved a schedule's target by name first.
// Matching on id alone would leave every name-armed schedule looking healthy.
func machineDeleted(owner, id, name string) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(id) == "" {
		return
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = id
	}
	// A deleted machine is shared with nobody. Dropped first: everything below
	// takes a moment, and a recipient resolving it in between would find the
	// index pointing at a record that is already gone.
	dropMachineShareIndex(id)
	gone := MachineDef{ID: id, Name: name}
	// Every user's schedules, not just the owner's — a shared machine can be
	// fired by somebody else's schedule, and the person who broke it never sees
	// the thing they broke.
	for _, u := range scheduleOwners(owner) {
		for _, sa := range ListStandingAgents(RootDB, u) {
			if !machineScheduleTargets(sa, gone) {
				continue
			}
			why := fmt.Sprintf("runs deleted machine %q", label)
			if u != owner {
				why = fmt.Sprintf("runs machine %q, which %s deleted", label, owner)
			}
			MarkStandingAgentBroken(RootDB, u, sa.Name, why)
		}
	}
	pruneDispatchTarget(owner, id)
}
