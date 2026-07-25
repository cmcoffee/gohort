// Namespace flatten migration — folds the embedded AgentRecord.Tools copies
// into the unified per-user tool store (PersistentTempTool.ScopeAgents).
//
// Before the flatten, one tool name could durably live in TWO homes — the
// user-wide pool AND an agent record — and the copies drifted (the Moltbook
// "Builder fixed it but the agent ran the other copy" failure). After it,
// name = key in ONE store and scope is data. This file is the one-time data
// move: every embedded record tool becomes a store row scoped to that agent.
//
// Conflict policy when a record tool's name already exists in the store:
//   - identical definition → the record copy is dropped (scope extended to
//     cover this agent when the store row is agent-scoped);
//   - diverged definition → the STORE copy wins (it is what the runtime has
//     actually been loading — behavior-preserving), and the record copy is
//     stashed in the orphan pool with its former-agent provenance so nothing
//     is silently destroyed. The user resolves the fork from Orphaned tools.
//
// The pre-migration embedded list is additionally backed up verbatim under
// toolFlattenBackupTable, keyed "<owner>:<agentID>" — never read by code,
// pure insurance.
//
// Runs from three triggers, all idempotent (a folded agent has empty Tools):
//   1. migrateAllAgentToolsToStore — startup sweep over every auth user.
//   2. loadAgentTempTools — lazy re-check when a turn loads an agent whose
//      record still carries embedded tools (e.g. written by an old binary).
//   3. importAgentRecipe — recipes exported before the flatten carry tools
//      inline; import folds them through the same path.

package orchestrate

import (
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const toolFlattenBackupTable = "orchestrate_tool_flatten_backup"

// foldAgentToolsIntoStore moves rec.Tools into the unified store and clears
// the field. Returns how many records were moved (new store rows), merged
// (identical dup dropped / scope extended), and orphaned (diverged dup
// stashed). The caller saves the record afterward.
func foldAgentToolsIntoStore(udb Database, owner string, rec *AgentRecord) (moved, merged, orphaned int) {
	if udb == nil || owner == "" || rec == nil || len(rec.Tools) == 0 {
		return 0, 0, 0
	}
	if isAppAgent(rec.ID) {
		// App agents never hold LLM-authored tools; a record that somehow
		// carries them is dropped from the runtime path but preserved in the
		// backup below for inspection.
		udb.Set(toolFlattenBackupTable, owner+":"+rec.ID, rec.Tools)
		rec.Tools = nil
		return 0, 0, 0
	}
	udb.Set(toolFlattenBackupTable, owner+":"+rec.ID, rec.Tools)
	for _, t := range rec.Tools {
		existing, ok := UserToolByName(udb, owner, t.Name)
		if !ok {
			if err := AdminPersistTempTool(udb, owner, t); err != nil {
				Log("[tool_flatten] %s/%s: persist %q failed (left in backup): %v", owner, rec.Name, t.Name, err)
				continue
			}
			SetUserToolScopeAgents(udb, owner, t.Name, []string{rec.ID})
			moved++
			continue
		}
		if tempToolDefEqual(t, existing.Tool) {
			if len(existing.ScopeAgents) > 0 && !existing.ScopedToAgent(rec.ID) {
				SetUserToolScopeAgents(udb, owner, t.Name,
					append(append([]string{}, existing.ScopeAgents...), rec.ID))
			}
			merged++
			continue
		}
		AddOrphanedTempTools(udb, owner, []OrphanedTempTool{{
			Tool:            t,
			FormerAgentID:   rec.ID,
			FormerAgentName: rec.Name,
			OrphanedAt:      time.Now(),
		}})
		Log("[tool_flatten] %s/%s: %q DIVERGED from the store copy — record copy stashed in Orphaned tools", owner, rec.Name, t.Name)
		orphaned++
	}
	rec.Tools = nil
	return moved, merged, orphaned
}

// migrateAgentToolsToStore folds one loaded agent and saves it when anything
// changed. Safe to call on every load path — a folded agent no-ops.
func migrateAgentToolsToStore(udb Database, owner string, rec *AgentRecord) {
	if rec == nil || len(rec.Tools) == 0 {
		return
	}
	moved, merged, orphaned := foldAgentToolsIntoStore(udb, owner, rec)
	if saved, err := saveAgent(udb, *rec); err == nil {
		*rec = saved
	} else {
		Log("[tool_flatten] %s/%s: save after fold failed: %v", owner, rec.Name, err)
	}
	Log("[tool_flatten] agent %q: %d moved, %d merged, %d orphaned", rec.Name, moved, merged, orphaned)
}

// migrateAllAgentToolsToStore sweeps every user's agents once at startup.
// Cheap when already migrated: one record walk per user, zero writes.
func migrateAllAgentToolsToStore(db Database) {
	for _, u := range AuthListUsers(db) {
		udb := agentUserDB(db, u.Username)
		if udb == nil {
			continue
		}
		for _, rec := range listAgents(udb, u.Username) {
			if len(rec.Tools) == 0 {
				continue
			}
			r := rec
			migrateAgentToolsToStore(udb, u.Username, &r)
		}
	}
}
