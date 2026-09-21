package orchestrate

// The mark on a turn a rule stopped, for the person it stopped.
//
// They get no card and no trail entry — see quietGuardrailFor for why a
// breadcrumb naming the rule, the hook and the warden's reason is the owner's.
// What they get is one glyph at the head of the reply, "This action was
// blocked." on hover, and a click that tells the owner they think it was wrong.
//
// A MARK rather than a message: the turn is already in front of them, so "this
// was stopped" and "say so if that is wrong" is the whole of what they can act
// on. It carries nothing about which rule or why.
//
// Rendered through the panel's generic per-message mark (ChatMessage.Mark), so
// it sits inline before the reply and survives a reload, and core/ui never
// learns what a guardrail is.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// blockedMarkAction is the client action the glyph runs. Registered by the app
// that draws the surface; see apps/agents.
const blockedMarkAction = "turn_blocked_report"

// blockedMarkTitle is the whole of what the mark says.
const blockedMarkTitle = "This action was blocked. Click if you think that is wrong."

// markTurnBlocked records that this turn was stopped, once.
//
// Once per TURN, not per block: a turn can trip the same rule at pre_action and
// again at pre_output, and two marks on one reply would say how many times it
// was caught, which is the detail this deliberately does not carry.
func (t *chatTurn) markTurnBlocked() {
	if t == nil || t.ranBy() == "" {
		return
	}
	t.toolMu.Lock()
	if t.blockMarked {
		t.toolMu.Unlock()
		return
	}
	t.blockMarked = true
	t.toolMu.Unlock()
}

// blockedMark is the mark to hang on this turn's reply, or nil.
func (t *chatTurn) blockedMark() *MessageMark {
	if t == nil || !t.blockMarked {
		return nil
	}
	sessionID := t.diagSessionID
	if t.session != nil {
		sessionID = t.session.ID
	}
	return &MessageMark{
		Glyph: "!", Title: blockedMarkTitle, Action: blockedMarkAction,
		Data: map[string]string{"session": sessionID, "agent": t.agent.ID},
	}
}

// markBlockedReply stamps the mark onto the reply this turn produced and tells
// the open pane about it. No-op on a turn no rule stopped.
//
// Both halves in ONE call, at every site that ends a turn, because there are
// three of them: a direct reply, a question, and the planned path. The first
// version delivered only at the third, which is the one a stopped turn never
// takes — the rejection writer produces a single sentence with no plan, so it
// leaves by the direct-reply return, and the mark was never sent or stored.
//
// The live event addresses no id: it is sent when the turn ends, so the last
// assistant bubble is the one it is about. A mark emitted when the rule fired
// would have had no bubble to attach to, since pre_output runs before the
// reply exists.
func (t *chatTurn) markBlockedReply(sess *ChatSession) {
	mark := t.blockedMark()
	if mark == nil {
		return
	}
	if sess != nil {
		for i := len(sess.Messages) - 1; i >= 0; i-- {
			if sess.Messages[i].Role == "assistant" {
				sess.Messages[i].Mark = mark
				break
			}
		}
	}
	if t.sse != nil {
		t.sse.Send(map[string]any{"kind": "message_mark", "mark": mark})
	}
}

// blockedTurnReport is what the owner receives when somebody says a mark was
// wrong.
//
// The recipient's own words, plus the DIAGNOSTIC the block already recorded:
// which rule, at which hook, and what the warden objected to. Not the turn.
//
// Two reasons it is not the turn. It would be the recipient's conversation,
// handed to the owner because a rule of theirs fired in it — a leak in the
// direction nothing here was watching, since everything else is about what
// travels the other way. And it would be redundant: the owner cannot read the
// thread anyway, and what they have to judge is whether the rule should have
// caught this, which is what the reason says.
//
// Read from the OWNER's block log, where recordGuardrailBlock filed it. So the
// report cannot be forged by the person sending it: they supply a session id
// and a sentence, and every word about the rule comes from the owner's store.
func blockedTurnReport(said, agentName string, blocks []GuardrailBlock) string {
	var b strings.Builder
	if said = strings.TrimSpace(said); said != "" {
		b.WriteString(said + "\n\n")
	}
	b.WriteString("They were running \"" + agentName + "\" and think this was stopped in error.\n")
	if len(blocks) == 0 {
		b.WriteString("\nThe matching entry is no longer in this agent's block log. Its Rules page has the recent ones.")
		return b.String()
	}
	b.WriteString("\nWhat stopped it:\n")
	for _, k := range blocks {
		b.WriteString("- " + chFirst(k.Rule, "(rule not recorded)"))
		if k.Hook != "" {
			b.WriteString(" at " + k.Hook)
		}
		if k.Reason != "" {
			b.WriteString(": " + truncateObs(k.Reason, 300))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// blocksForTurn pulls this session's recent entries out of the owner's log.
//
// Keyed by session rather than by a mark id: a turn can trip more than one
// rule, the owner is judging the decision rather than one interception, and
// the log already carries the session on every row.
func blocksForTurn(ownerDB Database, agentID, sessionID string, max int) []GuardrailBlock {
	sessionID = strings.TrimSpace(sessionID)
	if ownerDB == nil || sessionID == "" {
		return nil
	}
	var out []GuardrailBlock
	for _, k := range listGuardrailBlocks(ownerDB, agentID, guardrailLogKept) {
		if k.Session != sessionID {
			continue
		}
		out = append(out, k)
		if len(out) >= max {
			break
		}
	}
	return out
}
