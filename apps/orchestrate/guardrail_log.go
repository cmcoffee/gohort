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
//
// With one addition, for the one case the log alone cannot cover: a block on
// SOMEBODY ELSE'S run of a shared agent. The owner is not in that conversation
// and has no reason to open this log, and the person who was stopped cannot
// change the rule — so the block lands where neither of them is looking and the
// recipient quietly concludes the agent is broken. That one files a notice as
// well, folded per (agent, runner) so a rule tripping eleven times is one row
// with a count. Still not an interruption: it is a row on the surface the owner
// already checks, not an alert on their phone.

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
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

	// RanBy is the signed-in account whose turn this was, recorded only when it
	// was not the owner's — a colleague running an agent shared with them.
	// Empty on the owner's own runs, which is every run on an unshared agent,
	// so an existing log reads exactly as it did.
	//
	// NOT the same question as Sender. Sender is a contact's self-reported name
	// off a channel and proves nothing; this is an account the server
	// authenticated, and it is the difference between "a stranger messaged the
	// agent" and "the person you shared this with cannot get past your rule".
	RanBy string `json:"ran_by,omitempty"`

	// Tool names the tool whose RESULT was flagged, for entries filed by the
	// injection scanner (Hook == GuardHookToolResult). Empty for rule blocks,
	// which are about something the agent was going to do rather than
	// something a tool brought back.
	Tool string `json:"tool,omitempty"`
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
	entry := GuardrailBlock{
		At:      time.Now(),
		Rule:    strings.TrimSpace(rule),
		Hook:    strings.TrimSpace(hook),
		Reason:  strings.TrimSpace(reason),
		Channel: strings.TrimSpace(t.requesterChannel),
		Sender:  strings.TrimSpace(t.requesterName),
		Session: session,
		RanBy:   t.ranBy(),
	}
	appendGuardrailBlock(db, t.agent.ID, entry)
	t.tellOwnerAboutABlock(entry)
}

// ranBy names the account driving this turn when it is not the owner's own.
//
// Empty is the answer for every ordinary run, and deliberately so: the field
// exists to mark the case where the person who hit the rule and the person who
// wrote it are different people.
//
// A channel inbound is excluded although its identity also differs. It runs as
// "phantom:<chatID>", which names a conversation rather than a person, and
// Sender / Channel already carry what is actually known about that requester —
// putting the synthetic id here would read as an account somebody signed into.
func (t *chatTurn) ranBy() string {
	if t == nil {
		return ""
	}
	owner, user := strings.TrimSpace(t.ownerUser), strings.TrimSpace(t.user)
	if owner == "" || user == "" || owner == user || isSyntheticRequester(user) {
		return ""
	}
	return user
}

// tellOwnerAboutABlock files the notice described in this file's header, for a
// block on somebody else's run and nothing else.
//
// The owner gets the particulars — which rule, at which hook, what the warden
// objected to — because the rule is theirs. The person who was stopped gets
// none of it: they are told the agent could not do the thing, which is the fact
// they need, and not the rule that stopped them, which is the map of the fence.
func (t *chatTurn) tellOwnerAboutABlock(b GuardrailBlock) {
	if t == nil || b.RanBy == "" || RootDB == nil {
		return
	}
	owner := strings.TrimSpace(t.ownerUser)
	if owner == "" {
		return
	}
	name := chFirst(t.agent.Name, t.agent.ID)
	// The title is the fold key (owner, agent, kind, title), so it names the
	// two things that make one situation and leaves the particulars to the
	// body, which the newest occurrence overwrites.
	// Reads right at one and at eleven, because the row carries a count beside
	// it: "was stopped" with a 11 says what happened, where "keeps hitting"
	// would be a sentence that is wrong the first time it appears.
	title := b.RanBy + " was stopped by your rules on \"" + name + "\""
	if b.Hook == GuardHookToolResult {
		// A detection is not a rule doing its job; it is something that
		// arrived. Different sentence, and a different fold, so the two never
		// collapse into one row.
		title = "Something on " + b.RanBy + "'s run of \"" + name + "\" carried hidden instructions"
	}
	var body strings.Builder
	if b.Tool != "" {
		body.WriteString("Found in what " + b.Tool + " returned.\n")
	}
	body.WriteString("Rule: " + chFirst(b.Rule, "(not recorded)") + "\n")
	if b.Hook != "" {
		body.WriteString("Stopped at: " + b.Hook + "\n")
	}
	if b.Reason != "" {
		body.WriteString("Why: " + b.Reason + "\n")
	}
	body.WriteString("\n" + b.RanBy + " is using your agent \"" + name +
		"\", which they cannot change. They were told it could not do the thing, " +
		"not which rule stopped them. If this is the rule working, there is nothing to do; " +
		"if it is catching work you meant them to be able to do, the rule is yours to narrow.")
	notices.Record(RootDB, notices.Notice{
		Owner: owner,
		Agent: t.agent.ID,
		// Stopped, not Blocked: nothing is queued waiting on the owner. The
		// rule did what it was written to do, and the only open question is
		// whether they still want it doing it.
		Kind:  notices.KindStopped,
		Title: title,
		Body:  body.String(),
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
