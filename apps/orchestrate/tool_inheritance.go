// Parent-tool inheritance for dispatched Builder + owned sub-agents.
//
// Two surfaces use this:
//   - A Fleet parent (Chat) dispatching Builder: the dispatched Builder
//     inherits the parent's inheritable catalog so it can INSPECT the parent's
//     world (e.g. read a phantom chat) while authoring.
//   - An owned sub-agent with InheritParentTools set: at runtime it resolves
//     the parent's inheritable catalog in addition to its own AllowedTools, so
//     a Builder-authored summarizer can actually read the chat it summarizes.
//
// "Inheritable" is deliberately the NON-consequential slice of the parent:
// its normal worker tools (resolveWorkerTools with forOrchestrator=false skips
// the Fleet block, so delegate / message_contact / notify_me / standing-agent /
// monitor management never come along) PLUS the read-only phantom tools
// (list_phantom_chats, read_phantom_chat). The result can OBSERVE but not act
// on the owner's behalf — no texting people, no running the fleet.

package orchestrate

import . "github.com/cmcoffee/gohort/core"

// phantomReadOnlyToolDefs returns just the read-only phantom tools, filtered out
// of the full management set. Reuses operatorManagementTools as the canonical
// source (same pattern the watch-tool invoker uses in operator_wake.go) so the
// closures stay in one place and can't drift.
func phantomReadOnlyToolDefs(sess *ToolSession, agentID string) []AgentToolDef {
	var out []AgentToolDef
	for _, td := range operatorManagementTools(sess, agentID) {
		switch td.Tool.Name {
		case "list_phantom_chats", "read_phantom_chat":
			out = append(out, td)
		}
	}
	return out
}

// inheritableParentTools builds the parent agent's non-consequential catalog:
// its worker tools (no Fleet block) plus the read-only phantom tools. Closures
// are built against sess so they capture the running user/db. Returns nil on a
// resolution error rather than failing the caller (inheritance is additive).
func (t *chatTurn) inheritableParentTools(parent AgentRecord, sess *ToolSession) []AgentToolDef {
	// A minimal chatTurn for the parent so resolveWorkerTools resolves against
	// the PARENT's AllowedTools. forOrchestrator=false omits the Fleet block,
	// which is exactly what keeps the consequential tools out.
	pt := &chatTurn{
		app:     t.app,
		agent:   parent,
		user:    t.user,
		udb:     t.udb,
		ctx:     t.ctx,
		topic:   t.resolveTopic(),
		network: t.network,
	}
	tools, _, err := pt.resolveWorkerTools(sess, false)
	if err != nil {
		tools = nil
	}
	tools = append(tools, phantomReadOnlyToolDefs(sess, parent.ID)...)
	return tools
}

// mergeToolsDedup appends src onto dst, skipping any tool whose name is already
// present. Used so inherited tools don't double-register names the sub-agent /
// dispatched Builder already has (workspace, web_search, etc.).
func mergeToolsDedup(dst, src []AgentToolDef) []AgentToolDef {
	have := make(map[string]bool, len(dst))
	for _, td := range dst {
		have[td.Tool.Name] = true
	}
	for _, td := range src {
		if have[td.Tool.Name] {
			continue
		}
		have[td.Tool.Name] = true
		dst = append(dst, td)
	}
	return dst
}
