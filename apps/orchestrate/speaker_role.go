package orchestrate

// Who the agent is talking to, said to it.
//
// What each person may do with somebody else's agent is enforced in code:
// a recipient reaches a credential only through the shared tools, a maintainer
// edits the owner's record only within applyMaintainerEdit's list, Builder
// answers to the owner alone. The model was told none of it, so a refusal
// arrived with no reason it could give, and a person asking to change the
// agent got an attempt that failed instead of "that is the owner's to change".
//
// The role is read from the server's own facts, never from the message: the
// turn's owner is set only when the agent's owner is not the person signed in
// (runner_http.go), and the maintainer list is the owner's record. The note
// rides the newest user turn (AgentLoopConfig.TurnNotes), so it costs the
// prompt cache nothing and is not saved into the conversation. The owner gets
// none: an agent talking to its owner is the ordinary case.

import "strings"

// speakerRoleNote is the turn note naming the role of the person in this
// conversation, or "" for the owner and for runs that are not a signed-in
// person in the web chat.
func (t *chatTurn) speakerRoleNote() string {
	if t == nil {
		return ""
	}
	owner, user := strings.TrimSpace(t.ownerUser), strings.TrimSpace(t.user)
	// The owner, or a turn with no separate owner, is the ordinary case. A
	// channel inbound runs as the owner's account whoever sent it and has its
	// own audience handling (requester), so it is left to that.
	if owner == "" || user == "" || owner == user || t.requesterChannel != "" {
		return ""
	}
	if userMaintainsAgent(t.agent, user) {
		return frameworkNoteTag + "WHO YOU ARE TALKING TO: " + user + " maintains this agent for its owner, " + owner + ". They may change its instructions, standing rules and behaviour, and take tools, skills or collections off it. Adding any, its credentials, guardrails, memory, privacy and spending settings, and who uses or maintains it stay the owner's: when asked for one of those, say it is " + owner + "'s to change."
	}
	return frameworkNoteTag + "WHO YOU ARE TALKING TO: " + user + " is someone " + owner + ", this agent's owner, shared it with. They use it; they cannot change how it is set up. When asked to change it (its instructions, tools, or who it reaches), say that is " + owner + "'s to change rather than trying."
}
