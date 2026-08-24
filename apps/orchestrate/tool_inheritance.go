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

// phantomInheritableToolDefs returns the OWNER-SAFE tools an inheriting
// sub-agent / dispatched Builder may use: the read-only chat pair (list_chats,
// read_chat) PLUS notify_me, which only ever texts the OWNER (no approval, no
// third party) — so a scheduled summarizer can deliver its result to the user's
// phone. The genuinely consequential tools that reach OTHER people
// (send_message, message_contact, converse_with_contact) are NOT inheritable.
//
// TWO sources, which is the bug this carried for a long time. notify_me comes
// from operatorManagementTools; the chat readers come from channelChatTools.
// The old filter matched all three names against operatorManagementTools alone,
// which builds 18 tools and neither reader is among them — so it returned
// notify_me by itself and "a Builder-authored summarizer can read the chat it
// summarizes", the stated purpose of this file, never once worked through
// inheritance. operator_wake.go, cited here as "the same pattern", does the
// second lookup this was missing and says why: without it the watch fails
// "read_chat is not registered".
//
// It also named the readers list_phantom_chats / read_phantom_chat, which are
// not tool names anywhere in the tree — only in comments like this one. They
// are list_chats and read_chat.
func phantomInheritableToolDefs(sess *ToolSession, owner, agentID string) []AgentToolDef {
	var out []AgentToolDef
	for _, td := range operatorManagementTools(sess, agentID) {
		if td.Tool.Name == "notify_me" {
			out = append(out, td)
		}
	}
	// The chat readers come from channelChatTools, NOT from
	// operatorManagementTools — which builds 18 tools and neither of these is
	// among them. The old filter matched all three names against that one set,
	// so it returned notify_me alone and the read-the-chat half of this
	// function's whole purpose never fired. operator_wake.go, cited above as
	// "the same pattern", does the second lookup this was missing.
	//
	// Scoped to the agent's own channels, and read-only by construction:
	// channelChatTools returns list_chats / read_chat / send_message, and the
	// sender is dropped here. An inheriting sub-agent may READ the chat it was
	// built to summarize; reaching other people stays with the parent.
	for _, td := range channelChatTools(sess, owner, agentID) {
		switch td.Tool.Name {
		case "list_chats", "read_chat":
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
	// The OWNER's identity, not the runtime one: a phantom/channel run has a
	// synthetic per-chat user whose store holds neither the parent's channels
	// nor its tools, and the parent's chat is the thing being inherited.
	_, ownerUser := t.ownerView()
	tools = append(tools, phantomInheritableToolDefs(sess, ownerUser, parent.ID)...)
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
