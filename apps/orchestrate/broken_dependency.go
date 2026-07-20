package orchestrate

import (
	"fmt"
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
// static chat-tool registry, the shared deployment pool, the owner's global
// pool, or any of the owner's agents' scoped tools. Mirrors the credential-tool
// scan (credential_tools.go) so a tool that genuinely still exists is never
// flagged missing.
func toolResolvable(owner, name string) bool {
	if _, ok := LookupChatTool(name); ok {
		return true
	}
	for _, pt := range LoadSharedPersistentTempTools(RootDB) {
		if pt.Tool.Name == name {
			return true
		}
	}
	for _, pt := range LoadPersistentTempTools(RootDB, owner) {
		if pt.Tool.Name == name {
			return true
		}
	}
	for _, a := range listAgents(agentUserDB(RootDB, owner), owner) {
		for _, tt := range a.Tools {
			if tt.Name == name {
				return true
			}
		}
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

// standingAgentDependencyError returns a reason a standing agent can no longer
// run — its target agent was deleted — or "" if healthy.
func standingAgentDependencyError(sa StandingAgent) string {
	if !agentExists(sa.Owner, sa.AgentID) {
		return fmt.Sprintf("its target agent was deleted (id %s)", sa.AgentID)
	}
	return ""
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
}
