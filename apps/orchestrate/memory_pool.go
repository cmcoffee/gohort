package orchestrate

// Pooled memory: a shared specialist learns from everyone who uses it
// (docs/design/pooled-memory.md).
//
// Without it, each person who uses a shared agent learns into their own copy,
// and what one of them works out never reaches the next. On an agent whose
// MemoryPool is on, what a run works out (its findings) goes into the OWNER's
// copy instead, which everyone it is shared with already reads beneath their
// own. The pool adds a writer, not a store.
//
// Only findings pool. Pinned notes, the activity feed and working notes stay
// with the person, because they are about the person.
//
// Each pooled finding carries who it came from (EmbeddedChunk.By), and the
// rules on removing one are enforced here, not asked of the model: anyone may
// remove what they contributed; only the owner and the agent's maintainers
// remove anyone else's or clear the pool.

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/shareledger"
)

const (
	memoryPoolOn  = "on"
	memoryPoolOff = "off"
	// poolDailyCap is how many findings one person adds to a pool in a day.
	// Past it a finding is saved to their own copy instead, so a runaway loop
	// or one person cannot flood what everyone else reads.
	poolDailyCap = 50
)

// poolsMemory reports whether what this agent works out in other people's runs
// goes into its owner's copy. An app's agent never pools: it follows its app's
// own Memory setting.
func (a AgentRecord) poolsMemory() bool {
	return a.MemoryPool == memoryPoolOn && strings.TrimSpace(a.OwningApp) == ""
}

// newAgentMemoryPool is the MemoryPool a new agent is created with: on for a
// specialist, whose memory is know-how, and off for a chatbot or an assistant,
// whose memory is about the person it talks to.
func newAgentMemoryPool(a AgentRecord) string {
	if a.MemoryMode == memoryModeAgent {
		return memoryPoolOn
	}
	return memoryPoolOff
}

// settleNewAgentMemoryPool writes the pool setting onto an agent being created,
// once, so it is a visible choice from the first day rather than a default that
// can change under the people using it. An agent already stored, a framework
// agent and an app's agent are left as they are.
func settleNewAgentMemoryPool(db Database, a *AgentRecord) {
	if a.MemoryPool != "" || isSeedID(a.ID) || strings.TrimSpace(a.OwningApp) != "" {
		return
	}
	if a.ID != "" && db != nil {
		var stored AgentRecord
		if db.Get(agentsTable, a.ID, &stored) {
			return
		}
	}
	a.MemoryPool = newAgentMemoryPool(*a)
}

// managesPool reports whether user may curate an agent's pool: remove anyone's
// finding, clear it, remove everything one person added. Its owner and its
// maintainers.
func managesPool(a AgentRecord, user string) bool {
	user = strings.TrimSpace(user)
	return user != "" && (user == a.Owner || userMaintainsAgent(a, user))
}

// poolOwner is the owner whose copy this turn's findings go into, or empty
// when they go into the runner's own copy as before: the agent does not pool,
// this is the owner's own run, a clean room, a framework agent, or a person
// outside gohort, whose findings stay in their own store.
func (t *chatTurn) poolOwner() string {
	if t == nil || !t.agent.poolsMemory() || t.outsideAudience() {
		return ""
	}
	return t.memoryUnderlay()
}

// poolPrefix is the pool's chunk namespace when this turn reads one, or empty.
func (t *chatTurn) poolPrefix() string {
	owner := t.poolOwner()
	if owner == "" {
		return ""
	}
	return knowledgeSource(owner, t.agent.ID, "")
}

// findingTarget is where a finding this turn saves goes: whose copy, and who it
// is recorded as coming from (empty when the copy is the runner's own). note
// says why a finding meant for the pool went to the runner's copy instead.
func (t *chatTurn) findingTarget() (user, by, note string) {
	owner := t.poolOwner()
	if owner == "" {
		return t.user, "", ""
	}
	if n := poolContributionsToday(owner, t.agent.ID, t.user, time.Now()); n >= poolDailyCap {
		Log("[orchestrate.memory.pool] user=%q agent=%q reached the daily cap of %d pooled findings; saving to their own copy", t.user, t.agent.ID, poolDailyCap)
		t.tellOwnerCapReached(owner)
		return t.user, "", fmt.Sprintf(" This one is saved to your own memory only: you have added %d findings to what everyone using this agent shares today, which is the most in a day.", poolDailyCap)
	}
	return owner, t.user, ""
}

// poolContributionsToday counts the findings by has added to owner's pool for
// agentID in the last 24 hours, one per finding however many chunks it split
// into.
func poolContributionsToday(owner, agentID, by string, now time.Time) int {
	if VectorDB == nil || strings.TrimSpace(by) == "" {
		return 0
	}
	prefix := knowledgeSource(owner, agentID, "")
	since := now.Add(-24 * time.Hour)
	seen := map[string]bool{}
	for _, c := range ChunksWhere(VectorDB, func(c EmbeddedChunk) bool {
		return c.By == by && sourceInScope(c.Source, prefix)
	}) {
		if at, err := time.Parse(time.RFC3339, c.Date); err == nil && at.Before(since) {
			continue
		}
		seen[c.ReportID] = true
	}
	return len(seen)
}

// markFindingBy records who a finding came from on every chunk it was split
// into. A no-op for a finding in its contributor's own copy.
func markFindingBy(reportID, by string) {
	if VectorDB == nil || reportID == "" || strings.TrimSpace(by) == "" {
		return
	}
	var marked []EmbeddedChunk
	for _, c := range ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return c.ReportID == reportID }) {
		c.By = by
		marked = append(marked, c)
	}
	PutChunks(VectorDB, marked)
}

// labelPoolHits marks each finding another person contributed, so the reader
// takes it as something a colleague worked out, to check, rather than as the
// agent's own or an instruction. The reader's own contributions and the
// owner's are left plain.
func (t *chatTurn) labelPoolHits(hits []SearchHit) []SearchHit {
	for i, h := range hits {
		if h.By != "" && h.By != t.user {
			hits[i].Text = poolLabel(h.By) + h.Text
		}
	}
	return hits
}

// poolLabel opens a finding someone else contributed to a pool.
func poolLabel(by string) string {
	return "From " + by + ": "
}

// poolRemovalRefusal says why user may not remove chunk c from a's pool, or
// returns "" when they may: a manager removes anything, anyone else only what
// they added.
func poolRemovalRefusal(a AgentRecord, user string, c EmbeddedChunk) string {
	if managesPool(a, user) || (c.By != "" && c.By == user) {
		return ""
	}
	who := c.By
	if who == "" {
		who = "the owner"
	}
	return "That was added by " + who + "; only the owner or a maintainer can remove it."
}

// inPool reports whether chunk c is a finding in this turn's pool.
func (t *chatTurn) inPool(c EmbeddedChunk) bool {
	prefix := t.poolPrefix()
	return prefix != "" && sourceInScope(c.Source, prefix) && chunkProvenance(c.Source, c.ReportID) == "derived"
}

// pinnedNoteStaysYours is said after a pinned note saved by someone using a
// pooled agent they do not manage: what it learns is shared, but their pinned
// notes are not.
func (t *chatTurn) pinnedNoteStaysYours() string {
	if t.poolOwner() == "" || managesPool(t.agent, t.user) {
		return ""
	}
	return " This note is yours only: what this agent learns is shared with everyone who uses it, but notes you pin stay with you."
}

// tellOwnerCapReached tells the owner that someone has reached the day's limit
// on what they add to the pool. One notice per person: the notice store folds
// a repeat into a count rather than a second notice.
func (t *chatTurn) tellOwnerCapReached(owner string) {
	if RootDB == nil {
		return
	}
	name := chFirst(t.agent.Name, t.agent.ID)
	n := notices.Notice{
		Agent: t.agent.ID, Kind: notices.KindReport,
		Title: t.user + " reached the day's limit on what they add to " + name,
		Body: fmt.Sprintf("%s has added %d findings in a day to what %s has learned from everyone who uses it. "+
			"Anything more today is saved to their own memory only. A run stuck in a loop is the usual cause; "+
			"you can see and remove what they added in the agent's Memory window.", t.user, poolDailyCap, name),
	}
	if t.app != nil {
		t.app.notify(owner, n)
		return
	}
	n.Owner = owner
	notices.Record(RootDB, n)
}

// tellPoolChanges tells the people who have just come under an agent's pooled
// memory what that means for them: everyone it is shared with, when pooling
// has just been turned on, and anyone just added, when it was already on. A
// maintainer added later hears it in their maintainer notice instead
// (tellNewMaintainers). prior is the stored record before this save, nil for
// a new agent.
func tellPoolChanges(prior *AgentRecord, a AgentRecord) {
	if !a.poolsMemory() || shareledger.NotifyRecipient == nil {
		return
	}
	users := ShareMembers(a.AllowedUsers, a.Owner)
	maintainers := ShareMembers(a.Maintainers, a.Owner)
	had := map[string]bool{}
	turnedOn := prior == nil || !prior.poolsMemory()
	if !turnedOn {
		for _, u := range ShareMembers(append(append([]string{}, prior.AllowedUsers...), prior.Maintainers...), a.Owner) {
			had[u] = true
		}
		maintainers = nil
	}
	name := chFirst(a.Name, a.ID)
	told := map[string]bool{}
	for _, u := range maintainers {
		told[u] = true
		shareledger.NotifyRecipient(u, name+" now shares what it learns",
			a.Owner+" has pooled what "+name+" learns: what it works out in anyone's conversation is shared with everyone who uses it. "+
				"As a maintainer you can curate it in its Memory window, removing any entry or all of them.", nil)
	}
	for _, u := range users {
		if had[u] || told[u] {
			continue
		}
		shareledger.NotifyRecipient(u, name+" shares what it learns with everyone who uses it",
			"What "+name+" works out in your conversations is shared with everyone who uses it, and theirs with you, from now on. "+
				"What it learned before stays yours, and notes you pin stay yours. "+
				"You can see and remove what you added in its Memory window.", nil)
	}
}
