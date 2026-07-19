// Pipeline scoping — the write side of "which agents can run this pipeline,"
// mirroring admin_tool_scope.go for tools. A pipeline is either GLOBAL (on
// PipelineDef.Global — reaches every agent minus DisabledPipelines opt-outs)
// or agent-scoped (listed in each agent's AttachedPipelines by ID). The scope
// pill drives the transitions; the "pipeline" ScopeProvider registered here
// lets the shared pill UI + HTTP handlers manage pipelines with zero new
// plumbing.
package orchestrate

import (
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterScopeProvider("pipeline", ScopeProvider{State: pipelineScopeState, Set: setPipelineScope})
}

// agentSeesGlobalPipeline reports whether a GLOBAL pipeline is on for an agent:
// simply "not in its deny-list." No allow-list dimension (unlike tools) — a
// pipeline isn't gated by AllowedTools.
func agentSeesGlobalPipeline(a AgentRecord, id string) bool {
	for _, d := range a.DisabledPipelines {
		if d == id {
			return false
		}
	}
	return true
}

// agentHasAttachedPipeline reports whether an agent carries the pipeline id in
// its AttachedPipelines (the agent-scoped case).
func agentHasAttachedPipeline(a AgentRecord, id string) bool {
	for _, p := range a.AttachedPipelines {
		if p == id {
			return true
		}
	}
	return false
}

// pipelineScopeState builds the pill picture for a pipeline across the owner's
// agents. name is the pipeline ID (or name — LoadPipelineDef accepts either).
func pipelineScopeState(db Database, owner, name string) (ToolScopeState, bool) {
	st := ToolScopeState{Name: name, Agents: []ToolScopeAgent{}}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return st, false
	}
	def, ok := LoadPipelineDef(udb, owner, name)
	if !ok {
		return st, false
	}
	st.Name = def.Name
	st.Global = def.Global
	for _, a := range listAgents(udb, owner) {
		// App agents aren't pipeline-scope targets — their kit is app-declared.
		// Keyed on identity (the other planes' Hidden proxy leaks for a visible
		// app agent).
		if isAppAgent(a.ID) {
			continue
		}
		on := agentHasAttachedPipeline(a, def.ID)
		if st.Global {
			on = agentSeesGlobalPipeline(a, def.ID)
		}
		st.Agents = append(st.Agents, ToolScopeAgent{ID: a.ID, Name: a.Name, On: on})
	}
	return st, true
}

// setPipelineScope applies one pill toggle. Transitions mirror setToolScope:
//
//	target=global, on=true  → promote to global (attach copies stripped)
//	target=global, on=false → demote: attach onto the currently-ON agents
//	target=<agent>, on       → global: un-deny / deny; scoped: attach / detach
func setPipelineScope(db Database, owner, name, target string, on bool) error {
	udb := agentUserDB(db, owner)
	if udb == nil {
		return fmt.Errorf("no agent store for user %q", owner)
	}
	def, ok := LoadPipelineDef(udb, owner, name)
	if !ok {
		return fmt.Errorf("pipeline %q not found", name)
	}

	if target == "global" {
		if on == def.Global {
			return nil
		}
		if on {
			// Promote: mark global, then strip every agent's explicit attach
			// (they now reach it via global) and clear stale deny entries.
			def.Global = true
			SavePipelineDef(udb, def)
			for _, a := range listAgents(udb, owner) {
				if isAppAgent(a.ID) {
					continue // never touch an app agent's kit
				}
				changed := false
				if agentHasAttachedPipeline(a, def.ID) {
					a.AttachedPipelines = removeString(a.AttachedPipelines, def.ID)
					changed = true
				}
				if containsString(a.DisabledPipelines, def.ID) {
					a.DisabledPipelines = removeString(a.DisabledPipelines, def.ID)
					changed = true
				}
				if changed {
					if _, err := saveAgent(udb, a); err != nil {
						return err
					}
				}
			}
			return nil
		}
		// Demote: bundle a per-agent attach onto every agent that currently
		// sees it (not denied), then clear global + deny-lists.
		for _, a := range listAgents(udb, owner) {
			if isAppAgent(a.ID) {
				continue // don't attach a pipeline onto an app agent
			}
			if !agentSeesGlobalPipeline(a, def.ID) {
				a.DisabledPipelines = removeString(a.DisabledPipelines, def.ID)
				if _, err := saveAgent(udb, a); err != nil {
					return err
				}
				continue
			}
			if !agentHasAttachedPipeline(a, def.ID) {
				a.AttachedPipelines = append(a.AttachedPipelines, def.ID)
				if _, err := saveAgent(udb, a); err != nil {
					return err
				}
			}
		}
		def.Global = false
		SavePipelineDef(udb, def)
		return nil
	}

	// Per-agent toggle. No Owner-field equality guard — the agent is already
	// scoped to the resolved store, and this admin-driven path must reach any
	// agent (incl. sub-agents whose .Owner is a parent) instead of rejecting
	// them with "not your agent".
	a, ok := loadAgent(udb, target)
	if !ok {
		return fmt.Errorf("agent %q not found", target)
	}
	if def.Global {
		// Enable = un-deny; disable = deny.
		if on {
			a.DisabledPipelines = removeString(a.DisabledPipelines, def.ID)
		} else if !containsString(a.DisabledPipelines, def.ID) {
			a.DisabledPipelines = append(a.DisabledPipelines, def.ID)
		}
		_, err := saveAgent(udb, a)
		return err
	}
	// Agent-scoped: attach/detach the ID.
	if on {
		if !agentHasAttachedPipeline(a, def.ID) {
			a.AttachedPipelines = append(a.AttachedPipelines, def.ID)
		}
	} else {
		a.AttachedPipelines = removeString(a.AttachedPipelines, def.ID)
	}
	_, err := saveAgent(udb, a)
	return err
}

// containsString reports whether s is in list.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// removeString returns list without any element equal to s (order preserved).
func removeString(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
