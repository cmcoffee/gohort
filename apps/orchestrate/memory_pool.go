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
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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

// Recently removed: nothing is lost from a pool by a click
// (docs/design/pooled-memory.md, slice 3).
//
// Every removal from a pooled agent's memory (one finding, everything one
// person added, a wipe) is kept for 30 days in the owner's store as one entry,
// holding the removed chunks as they were, vectors included, so a restore puts
// them back without embedding them again. The owner and the agent's
// maintainers see and restore every entry; anyone else sees and restores only
// what they removed themselves, which is only ever what they added.
//
// Only removals by a person or by the agent at their word are kept. The
// finding cap's eviction of the oldest is not a click, and keeping it would
// make this a second copy of the pool.

const (
	// poolRemovedTable holds the removals, in the pool owner's store, keyed
	// "<agent id>:<entry id>".
	poolRemovedTable = "orchestrate_pool_removed"
	// poolRemovedKeep is how long a removal can be restored.
	poolRemovedKeep = 30 * 24 * time.Hour
)

// poolRemoval is one removal from a pool, restorable as a whole.
type poolRemoval struct {
	ID        string
	AgentID   string
	RemovedBy string
	At        time.Time
	// What says what was removed, in words for the Memory window.
	What   string
	Chunks []EmbeddedChunk
}

// keepPoolRemoval records chunks about to be removed from a's pool, so they can
// be restored for 30 days. Only the chunks that are findings in the pool (the
// owner's copy) are kept, and nothing is kept for an agent that does not pool.
// db is the app's store; the entry goes in the owner's.
func keepPoolRemoval(db Database, a AgentRecord, removedBy, what string, chunks []EmbeddedChunk) {
	if db == nil || !a.poolsMemory() {
		return
	}
	prefix := agentKnowledgePrefix(a.Owner, a.ID)
	var kept []EmbeddedChunk
	for _, c := range chunks {
		if sourceInScope(c.Source, prefix) && chunkProvenance(c.Source, c.ReportID) == "derived" {
			kept = append(kept, c)
		}
	}
	udb := UserDB(db, a.Owner)
	if len(kept) == 0 || udb == nil {
		return
	}
	purgePoolRemovals(udb, time.Now())
	e := poolRemoval{ID: UUIDv4(), AgentID: a.ID, RemovedBy: removedBy, At: time.Now(), What: what, Chunks: kept}
	udb.Set(poolRemovedTable, a.ID+":"+e.ID, e)
	Log("[orchestrate.memory.pool] user=%q removed %d chunk(s) from %s's pool for agent=%s (%s); restorable for 30 days",
		removedBy, len(kept), a.Owner, a.ID, what)
}

// purgePoolRemovals drops every removal in udb older than poolRemovedKeep.
func purgePoolRemovals(udb Database, now time.Time) {
	for _, k := range udb.Keys(poolRemovedTable) {
		var e poolRemoval
		if !udb.Get(poolRemovedTable, k, &e) || now.Sub(e.At) > poolRemovedKeep {
			udb.Unset(poolRemovedTable, k)
		}
	}
}

// dropPoolRemovals forgets every removal kept for an agent being deleted.
func dropPoolRemovals(udb Database, agentID string) {
	for _, k := range udb.Keys(poolRemovedTable) {
		if strings.HasPrefix(k, agentID+":") {
			udb.Unset(poolRemovedTable, k)
		}
	}
}

// poolRemovalsFor is the removals from a's pool that user may see and restore,
// newest first: every one for the owner and its maintainers, otherwise only
// those user removed.
func poolRemovalsFor(udb Database, a AgentRecord, user string, now time.Time) []poolRemoval {
	purgePoolRemovals(udb, now)
	manage := managesPool(a, user)
	var out []poolRemoval
	for _, k := range udb.Keys(poolRemovedTable) {
		if !strings.HasPrefix(k, a.ID+":") {
			continue
		}
		var e poolRemoval
		if udb.Get(poolRemovedTable, k, &e) && (manage || e.RemovedBy == user) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// removedRow is one removal as the Memory window lists it.
type removedRow struct {
	ID        string    `json:"id"`
	What      string    `json:"what"`
	RemovedBy string    `json:"removed_by"`
	At        time.Time `json:"at"`
	Entries   int       `json:"entries"`
	Until     time.Time `json:"until"`
}

// poolRemovedOf is the agent a Recently removed call is about and its owner's
// store, where the removals are kept.
func (T *OrchestrateApp) poolRemovedOf(c *jsonapi.Call, at memAt) (AgentRecord, Database, error) {
	mc, err := T.memoryOf(c, at)
	if err != nil {
		return AgentRecord{}, nil, err
	}
	owner := strings.TrimSpace(mc.agent.Owner)
	if owner == "" || owner == seedOwner {
		return AgentRecord{}, nil, errNotFound
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return AgentRecord{}, nil, errNotFound
	}
	return mc.agent, udb, nil
}

// poolRemovedRoute lists the removals from an agent's pool the caller may
// restore.
//
//	GET /api/agents/{id}/inferred-removed → {items: [removedRow]}
func (T *OrchestrateApp) poolRemovedRoute(at memAt) http.HandlerFunc {
	return jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		a, udb, err := T.poolRemovedOf(c, at)
		if err != nil {
			return nil, err
		}
		rows := []removedRow{}
		for _, e := range poolRemovalsFor(udb, a, c.User, time.Now()) {
			rows = append(rows, removedRow{ID: e.ID, What: e.What, RemovedBy: e.RemovedBy, At: e.At,
				Entries: len(e.Chunks), Until: e.At.Add(poolRemovedKeep)})
		}
		return map[string]any{"items": rows}, nil
	})
}

// poolRestoreRoute puts one removal back where it was, as a whole. A removal
// the caller may not see is a 404, as if it were not there.
//
//	POST /api/agents/{id}/inferred-removed/{item} → {restored: N}
func (T *OrchestrateApp) poolRestoreRoute(at memAt) http.HandlerFunc {
	return jsonapi.Post(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
		a, udb, err := T.poolRemovedOf(c, at)
		if err != nil {
			return nil, err
		}
		id := at(c).item
		for _, e := range poolRemovalsFor(udb, a, c.User, time.Now()) {
			if e.ID != id {
				continue
			}
			PutChunks(VectorDB, e.Chunks)
			udb.Unset(poolRemovedTable, a.ID+":"+e.ID)
			Log("[orchestrate.memory.pool] user=%q restored %d chunk(s) to %s's pool for agent=%s (%s, removed by %s)",
				c.User, len(e.Chunks), a.Owner, a.ID, e.What, e.RemovedBy)
			return map[string]int{"restored": len(e.Chunks)}, nil
		}
		return nil, errNotFound
	})
}

// findingWhat names one removed finding for the Memory window.
func findingWhat(chunks []EmbeddedChunk) string {
	for _, c := range chunks {
		if t := strings.TrimSpace(strings.TrimPrefix(c.Section, "## ")); t != "" {
			return "A finding: " + t
		}
	}
	return "A finding"
}

// wipeWhat names a clear of the pool, or of what one person added to it.
func wipeWhat(by string) string {
	if by == "" {
		return "Everything it had learned"
	}
	return "Everything " + by + " added"
}

// keepPoolRemoval keeps the chunks a forget in this turn is about to remove,
// when they are findings in its agent's pool.
func (t *chatTurn) keepPoolRemoval(chunks []EmbeddedChunk) {
	if t == nil || t.app == nil {
		return
	}
	keepPoolRemoval(t.app.DB, t.agent, t.user, findingWhat(chunks), chunks)
}
