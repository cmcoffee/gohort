package orchestrate

// The mark on a turn a rule stopped, for the person it stopped.
//
// They get no card and no trail entry — see quietGuardrailFor for why a
// full-detail breadcrumb is the owner's and not theirs. What they get is that
// something was stopped, and a way to say they think it was wrong.
//
// It is a MARK rather than a message on purpose: one glyph, the sentence only
// on hover, and no statement about which rule or why. The turn is already in
// front of them, so "this was stopped" plus "tell the owner if that is wrong"
// is the whole of what they can act on.
//
// Emitted as a generic UI block, which is the toolkit's sanctioned route for an
// app-specific thing on screen: the renderer lives in the app (see
// apps/agents), and core/ui never learns what a guardrail is.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guardrailMarkBlock is the block type the app's renderer registers for.
const guardrailMarkBlock = "turn_blocked"

// markTurnBlocked puts the mark on this turn, once, for a run that is not the
// owner's.
//
// Once per TURN, not per block: a turn can trip the same rule at pre_action and
// again at pre_output, and two marks on one reply would say something about how
// many times it was caught, which is the detail this is deliberately not
// carrying.
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

	id := "blocked-" + UUIDv4()[:8]
	sessionID := t.diagSessionID
	if t.session != nil {
		sessionID = t.session.ID
	}
	// Hover text, and nothing else. Naming the rule here would undo the split
	// this whole path exists for.
	const title = "This action was blocked."
	payload := map[string]any{
		"kind":  "block",
		"type":  guardrailMarkBlock,
		"id":    id,
		"title": title,
		"data": map[string]string{
			"session": sessionID,
			"agent":   t.agent.ID,
		},
	}
	if t.sse != nil {
		t.sse.Send(payload)
	} else if sink := diagNoticeSinkFrom(t.ctx); sink != nil {
		sink.Send(payload)
	}
	// Persisted, so the mark survives a reload. A turn that was stopped is
	// still a turn that was stopped when you come back to the thread, and a
	// mark that vanishes reads as having imagined it.
	if t.session != nil {
		t.toolMu.Lock()
		t.session.upsert_ui_block(UIBlock{
			Type: guardrailMarkBlock, ID: id, Title: title,
			Data: map[string]string{"session": sessionID, "agent": t.agent.ID},
		}, nil)
		t.toolMu.Unlock()
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
// direction nobody was watching, since everything else here is about what
// travels the other way. And it would be redundant: the owner cannot read the
// thread anyway, and the thing they actually have to judge is whether the rule
// should have caught this, which is what the reason says.
//
// Read from the OWNER's block log, which is where recordGuardrailBlock filed
// it. So the report cannot be forged by the person sending it: they supply the
// session id and their sentence, and every word about the rule comes from the
// owner's own store.
func blockedTurnReport(said, agentName string, blocks []GuardrailBlock) string {
	var b strings.Builder
	if said = strings.TrimSpace(said); said != "" {
		b.WriteString(said + "\n\n")
	}
	b.WriteString("They were running \"" + agentName + "\" and think this was stopped in error.\n")
	if len(blocks) == 0 {
		// The mark is on the turn, so something stopped it; the log may have
		// rolled past it, or the block came from a path that files elsewhere.
		// Saying so beats implying the rule is unknown.
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
