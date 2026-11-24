package orchestrate

import (
	"strings"
	"testing"
)

// The agent is told who it is talking to when that is not its owner: a
// maintainer, or someone it was shared with. Nothing for the owner, and
// nothing for a channel inbound, which has its own audience handling.
func TestTheAgentIsToldWhoItIsTalkingTo(t *testing.T) {
	agent := AgentRecord{ID: "helper", Owner: "alice", Maintainers: []string{"carol"}}
	note := func(tt *chatTurn) string { return tt.speakerRoleNote() }

	if n := note(&chatTurn{agent: agent, user: "alice"}); n != "" {
		t.Errorf("the owner got a role note: %q", n)
	}
	if n := note(&chatTurn{agent: agent, user: "alice", ownerUser: "alice"}); n != "" {
		t.Errorf("the owner got a role note: %q", n)
	}
	m := note(&chatTurn{agent: agent, user: "carol", ownerUser: "alice"})
	if !strings.Contains(m, "carol maintains this agent") || !strings.Contains(m, "alice's to change") ||
		!strings.HasPrefix(m, frameworkNoteTag) {
		t.Errorf("a maintainer's note does not say they maintain it, or what stays the owner's: %q", m)
	}
	s := note(&chatTurn{agent: agent, user: "bob", ownerUser: "alice"})
	if !strings.Contains(s, "bob is someone alice") || !strings.Contains(s, "cannot change how it is set up") ||
		strings.Contains(s, "maintains") {
		t.Errorf("a shared user's note does not say they use it and cannot change it: %q", s)
	}
	if n := note(&chatTurn{agent: agent, user: "phantom:123", ownerUser: "alice", requesterChannel: "imessage"}); n != "" {
		t.Errorf("a channel inbound got a role note: %q", n)
	}
	for _, n := range []string{m, s} {
		if strings.Contains(n, "—") || strings.Contains(n, "…") {
			t.Errorf("a role note carries an em-dash or an ellipsis character: %q", n)
		}
	}
}

// The chat turn's turn notes carry the role note.
func TestTheChatTurnSaysWhoItIsTalkingTo(t *testing.T) {
	body := funcBody(t, readFile(t, "runner.go"), "func (pr *planRun) loopConfig() AgentLoopConfig {")
	if !strings.Contains(body, "t.speakerRoleNote()") {
		t.Error("the chat turn's notes no longer say who the agent is talking to")
	}
}
