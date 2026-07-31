package orchestrate

// The guardrail block log — one place the owner can review what their rules
// have actually stopped.
//
// The per-thread ⚠ trail already records every block, and it is the right
// record: it holds the full detail and it never leaves the deployment. What it
// is not is REVIEWABLE — you have to already know which thread to open, which
// on a channel is the one thing you don't, because the conversation happened on
// somebody else's phone.
//
// So the same block also lands here: a short, per-agent, append-only list the
// Rules modal shows underneath the rules themselves. Reviewing what a rule has
// done belongs next to the rule, not in a separate console.
//
// Nothing is sent anywhere. An earlier version of this pushed an alert to the
// owner's handle or email; the owner's answer was that a block is not an
// interruption, it is something to look at later. That also removes the reason
// the alert had to be redacted — this record stays on the box, so it can carry
// the warden's reason and the hook in full.

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// guardrailLogTable holds one capped list per agent.
const guardrailLogTable = "guardrail_blocks"

// guardrailLogKept is how many blocks are retained per agent. Enough to see a
// pattern (is one rule doing all the work? is something probing it?), short
// enough that the list stays readable and the record stays small.
const guardrailLogKept = 100

// GuardrailBlock is one recorded block.
type GuardrailBlock struct {
	At      time.Time `json:"at"`
	Rule    string    `json:"rule"`
	Hook    string    `json:"hook"`
	Reason  string    `json:"reason,omitempty"`
	Channel string    `json:"channel,omitempty"` // the surface it arrived on, when known
	Sender  string    `json:"sender,omitempty"`  // the contact's self-reported name — untrusted, shown as-is
	Session string    `json:"session,omitempty"` // the thread, so the full ⚠ trail can be found
}

// recordGuardrailBlock files a block for later review. Called on every block,
// including repeats: a rule tripping eleven times in a minute is exactly the
// shape worth seeing, and collapsing it would hide the thing the log is for.
func (t *chatTurn) recordGuardrailBlock(rule, hook, reason string) {
	if t == nil || strings.TrimSpace(rule) == "" {
		return
	}
	// The owner's store, for the same reason the ⚠ trail moved there: a channel
	// turn can run as a synthetic per-chat identity, and a record filed under
	// that identity is filed where nobody will look.
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	session := t.diagSessionID
	if t.session != nil {
		session = t.session.ID
	}
	appendGuardrailBlock(db, t.agent.ID, GuardrailBlock{
		At:      time.Now(),
		Rule:    strings.TrimSpace(rule),
		Hook:    strings.TrimSpace(hook),
		Reason:  strings.TrimSpace(reason),
		Channel: strings.TrimSpace(t.requesterChannel),
		Sender:  strings.TrimSpace(t.requesterName),
		Session: session,
	})
}

// appendGuardrailBlock adds one entry, trimming to the retention cap.
func appendGuardrailBlock(db Database, agentID string, entry GuardrailBlock) {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	var list []GuardrailBlock
	db.Get(guardrailLogTable, agentID, &list)
	list = append(list, entry)
	if n := len(list); n > guardrailLogKept {
		list = list[n-guardrailLogKept:]
	}
	db.Set(guardrailLogTable, agentID, list)
}

// listGuardrailBlocks returns an agent's recorded blocks, NEWEST FIRST and
// capped at limit — the order they are read in, since the question is almost
// always "what just happened".
func listGuardrailBlocks(db Database, agentID string, limit int) []GuardrailBlock {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	var list []GuardrailBlock
	db.Get(guardrailLogTable, agentID, &list)
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	out := make([]GuardrailBlock, 0, limit)
	for i := len(list) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, list[i])
	}
	return out
}

// clearGuardrailBlocks empties an agent's log — the owner acknowledging what
// they have read, so the next thing to appear is new.
func clearGuardrailBlocks(db Database, agentID string) {
	if db != nil && strings.TrimSpace(agentID) != "" {
		db.Unset(guardrailLogTable, agentID)
	}
}
