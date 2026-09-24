package orchestrate

// Memory that knows what time it is.
//
// A saved note about a trip ("just got back from Oregon") was true on a date,
// and an agent that keeps asking how the trip went a month later has not
// noticed the month. A saved note about pending work ("edits still pending on
// the photos") waits on somebody, and an agent that treats it as live keeps
// raising work nobody is doing. Both were stored as plain facts, so both were
// stamped on every prompt, and recall hints kept surfacing the trip because
// "it was good" reads a lot like a trip report.
//
// The save path now records what a note is about in time (core/provenance:
// MemKind). This file acts on it:
//
//   - An EVENT is ordinary memory while it is fresh. Past that, the daily pass
//     moves it out of the saved notes and into reference memory, rewritten as
//     dated past tense. Nothing is lost: it is still found by search. It just
//     stops being on every prompt, and recall hints stop offering it.
//   - An OPEN ITEM left idle is asked about ONCE, in the conversation. Saying
//     it again marks it live; forgetting it, or not answering, closes it as
//     "not pursued".
//
// Every move the pass makes is recorded, shown in the agent's Memory panel,
// and can be undone there. A pass that edits someone's memory in the dark is
// worse than the problem it solves.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/provenance"
)

const (
	tuneEventFreshDays = "tune_memory_event_fresh_days"
	tuneOpenIdleDays   = "tune_memory_open_idle_days"
	tuneOpenAnswerDays = "tune_memory_open_answer_days"

	memoryLifecycleKey = "memory_lifecycle"
	memoryMovesTable   = "memory_moves"

	// Chunk Kind tags on reference memory. "event" is a dated finding still
	// fresh; "past_event" one that has aged, which recall hints skip.
	chunkKindEvent     = "event"
	chunkKindPastEvent = "past_event"
)

func init() {
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneEventFreshDays, Category: "Memory",
		Label:  "Days an event stays in saved notes (0 = never move)",
		Help:   "After this many days, a saved note about an event moves to reference memory as dated past tense.",
		Detail: "An event is something that happened on a date: a trip, a visit, an appointment. Moved, it is still found by search but is no longer in every prompt or offered as a recall hint, so the agent stops bringing it up as news.",
		Kind:   KindInt, Default: 14, Min: 0, Max: 365})
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneOpenIdleDays, Category: "Memory",
		Label:  "Days before an idle open item is asked about (0 = never ask)",
		Help:   "An open item with no activity for this long is asked about once in the next conversation.",
		Detail: "An open item records work or a decision still waiting on someone (\"pending edits\"). Saying it again keeps it; declining closes it as not pursued.",
		Kind:   KindInt, Default: 14, Min: 0, Max: 365})
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneOpenAnswerDays, Category: "Memory",
		Label:  "Days to wait for an answer before closing an open item",
		Help:   "An open item that was asked about and not picked back up closes as not pursued after this many days.",
		Detail: "Closed items can be restored from the agent's Memory panel.",
		Kind:   KindInt, Default: 7, Min: 1, Max: 365})
	RegisterMaintenanceFunc("Housekeeping", memoryLifecycleKey, "Age out past events and stale open items",
		"Runs daily on its own. Moves saved notes about past events into reference memory, closes open items nobody picked back up after being asked, and dates event findings so recall hints stop offering old ones. Every move can be undone from the agent's Memory panel.",
		runMemoryLifecycle)
}

func eventFreshFor() time.Duration {
	return time.Duration(TuneInt(tuneEventFreshDays)) * 24 * time.Hour
}

func openIdleFor() time.Duration {
	return time.Duration(TuneInt(tuneOpenIdleDays)) * 24 * time.Hour
}

func openAnswerFor() time.Duration {
	return time.Duration(TuneInt(tuneOpenAnswerDays)) * 24 * time.Hour
}

// memoryMove is one thing the lifecycle moved out of the live notes, kept so
// the owner can see it and put it back.
type memoryMove struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	AgentID string    `json:"agent_id"`
	Kind    string    `json:"kind"` // "past_event" | "not_pursued"
	FactID  string    `json:"fact_id"`
	Note    string    `json:"note"`              // the saved note as it was
	Finding string    `json:"finding,omitempty"` // the reference-memory report a past event became
}

// lifecycleTally is what one pass did, for its outcome line.
type lifecycleTally struct {
	users, events, closed, dated int
}

func (lt lifecycleTally) moved() int { return lt.events + lt.closed }

// runMemoryLifecycle is the daily pass over every user's agent memory.
func runMemoryLifecycle(ctx context.Context) int {
	if orchestrateBaseDB == nil {
		return 0
	}
	now := time.Now()
	users := AuthListUsers(AuthDB())
	var lt lifecycleTally
	start, lastTick := time.Now(), time.Now()
	for i, u := range users {
		if ctx.Err() != nil {
			break
		}
		if time.Since(lastTick) >= 2*time.Second {
			lastTick = time.Now()
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d users - %d moved - %s",
				i, len(users), lt.moved(), time.Since(start).Round(time.Second)))
		}
		lifecycleForUser(ctx, u.Username, now, &lt)
		lt.users++
	}
	lt.dated = dateEventFindings(now)
	line := fmt.Sprintf("Checked %d users: %d past event(s) moved to reference memory, %d open item(s) closed, %d finding(s) dated",
		lt.users, lt.events, lt.closed, lt.dated)
	if ctx.Err() != nil {
		line = "Stopped early. " + line
	}
	ReportMaintenanceOutcome(ctx, line)
	Log("[orchestrate.memory.lifecycle] %s", line)
	return lt.moved() + lt.dated
}

// factNamespaces lists the agent memory namespaces a user has notes in.
func factNamespaces(udb Database) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range udb.Keys(MemoryFactsTable) {
		i := strings.LastIndexByte(k, '/')
		if i <= 0 || !strings.HasPrefix(k, "agent:") {
			continue
		}
		if ns := k[:i]; !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

func lifecycleForUser(ctx context.Context, user string, now time.Time, lt *lifecycleTally) {
	udb := UserDB(orchestrateBaseDB, user)
	if udb == nil {
		return
	}
	fresh, answer := eventFreshFor(), openAnswerFor()
	for _, ns := range factNamespaces(udb) {
		agentID := strings.TrimPrefix(ns, "agent:")
		for _, f := range ListMemoryFacts(udb, ns) {
			if ctx.Err() != nil {
				return
			}
			// A note saved before kinds were recorded is classified once, as
			// of when it was written, without touching its dates: the idle
			// clock of an old open item must not restart because it was read.
			if f.MemKind == provenance.MemKindUnknown {
				asOf := f.Created
				if asOf.IsZero() {
					asOf = now // no record of when: read it as today rather than as ancient
				}
				f.MemKind, f.EventAt = provenance.ClassifyMemKind(f.Note, asOf)
				udb.Set(MemoryFactsTable, ns+"/"+f.ID, f)
			}
			switch f.MemKind {
			case provenance.MemKindEvent:
				if fresh > 0 && now.Sub(f.EventDate(f.Created)) > fresh {
					if moveEventToReference(ctx, udb, user, agentID, f, now) {
						lt.events++
					}
				}
			case provenance.MemKindOpenItem:
				if !f.AskedAt.IsZero() && now.Sub(f.AskedAt) > answer {
					closeOpenItem(udb, agentID, f, now)
					lt.closed++
				}
			}
		}
	}
	pruneMemoryMoves(udb, now)
}

// pastTenseFinding rewrites an event note as one dated past-tense sentence,
// so the finding reads as history when search turns it up. The worker does
// the rewrite; without one, the note is kept word for word under a date.
func pastTenseFinding(ctx context.Context, note string, at time.Time) string {
	date := at.Format("2006-01-02")
	fallback := "Past event (around " + date + "): " + note
	app := orchRef
	if app == nil {
		return fallback
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := app.WorkerChat(cctx, []Message{{Role: "user", Content: fmt.Sprintf(
		"Rewrite this memory note as ONE short sentence in the past tense that says when it happened. Keep every name and detail in it and add nothing. The event was around %s.\n\nNote: %q\n\nReply with only the sentence.", date, note)}},
		WithThink(false), WithMaxTokens(160))
	if err != nil || resp == nil {
		return fallback
	}
	out := strings.Trim(strings.TrimSpace(ResponseText(resp)), "\"")
	// A rewrite that came back empty, ran long, or lost the date is not one to
	// trust with someone's memory.
	if out == "" || len(out) > 3*len(note)+80 || strings.Contains(out, "\n") {
		return fallback
	}
	if !strings.Contains(out, at.Format("2006")) && !strings.Contains(out, date) {
		out += " (around " + date + ")"
	}
	return out
}

// moveEventToReference turns a saved event note into a dated finding and
// retires the note, recording the move. Reports whether it moved.
func moveEventToReference(ctx context.Context, udb Database, user, agentID string, f MemoryFact, now time.Time) bool {
	if VectorDB == nil {
		return false // nowhere to put it; leaving it is better than losing it
	}
	at := f.EventDate(f.Created)
	text := pastTenseFinding(ctx, f.Note, at)
	ictx, cancel := context.WithTimeout(ctx, knowledgeIngestTimeout())
	defer cancel()
	reportID := ingestAgentKnowledge(ictx, orchestrateBaseDB, user, agentID, "", "Past event", text)
	if reportID == "" {
		return false
	}
	tagFindingChunks(reportID, chunkKindPastEvent, at, f.MemoryProvenance)
	f.Reason, f.RetiredAt, f.Successor, f.Updated = provenance.RetirePast, now, reportID, now
	udb.Set(MemoryFactsTable, factsNamespace(agentID)+"/"+f.ID, f)
	recordMemoryMove(udb, memoryMove{AgentID: agentID, Kind: "past_event", FactID: f.ID, Note: f.Note, Finding: reportID}, now)
	return true
}

// closeOpenItem retires an open item as not pursued and records it.
func closeOpenItem(udb Database, agentID string, f MemoryFact, now time.Time) {
	f.Reason, f.RetiredAt, f.Successor, f.Updated = provenance.RetireNotPursued, now, "", now
	udb.Set(MemoryFactsTable, factsNamespace(agentID)+"/"+f.ID, f)
	recordMemoryMove(udb, memoryMove{AgentID: agentID, Kind: "not_pursued", FactID: f.ID, Note: f.Note}, now)
}

// tagFindingChunks stamps a finding's chunks as an event (fresh or past) with
// the date it happened, carrying the note's origin across.
func tagFindingChunks(reportID, kind string, at time.Time, from MemoryProvenance) int {
	n := 0
	for _, c := range ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return c.ReportID == reportID }) {
		c.Kind = kind
		c.MemKind, c.EventAt = provenance.MemKindEvent, at
		// The chunk's own Source is its corpus; the note's origin is the
		// provenance one underneath it.
		if from.Source != MemSourceUnknown {
			c.MemoryProvenance.Source = from.Source
		}
		VectorDB.Set(EmbeddedChunks, c.ID, c)
		n++
	}
	if n > 0 {
		InvalidateChunkCache()
	}
	return n
}

// dateEventFindings walks the saved findings and (1) classifies ones written
// before events were recognised, (2) marks a dated event past once it has aged,
// so recall hints stop offering it. Returns how many it changed.
func dateEventFindings(now time.Time) int {
	if VectorDB == nil {
		return 0
	}
	fresh := eventFreshFor()
	changed := 0
	for _, c := range ChunksWhere(VectorDB, func(c EmbeddedChunk) bool {
		return strings.HasPrefix(c.Source, "orchestrate:") && strings.HasPrefix(c.ReportID, "orch-know-") &&
			(c.Kind == "" || c.Kind == chunkKindEvent)
	}) {
		orig := c.Kind
		written := chunkWritten(c)
		if c.Kind == "" && c.MemKind == provenance.MemKindUnknown {
			kind, at := provenance.ClassifyMemKind(c.Text, written)
			if kind != provenance.MemKindEvent {
				continue
			}
			c.Kind, c.MemKind, c.EventAt = chunkKindEvent, kind, at
		}
		if c.Kind == chunkKindEvent && fresh > 0 && now.Sub(c.EventDate(written)) > fresh {
			c.Kind = chunkKindPastEvent
		}
		if c.Kind != orig {
			VectorDB.Set(EmbeddedChunks, c.ID, c)
			changed++
		}
	}
	if changed > 0 {
		InvalidateChunkCache()
	}
	return changed
}

// chunkWritten is when a chunk was ingested, from its RFC3339 Date.
func chunkWritten(c EmbeddedChunk) time.Time {
	if t, err := time.Parse(time.RFC3339, c.Date); err == nil {
		return t
	}
	return time.Now()
}

// pastEventHit reports whether a search hit is an event that has aged: a
// finding the pass already marked, or a dated one it has not reached yet.
// Recall hints skip these; a search the agent or user runs still finds them.
func pastEventHit(h SearchHit, now time.Time) bool {
	switch h.Kind {
	case chunkKindPastEvent:
		return true
	case chunkKindEvent:
		fresh := eventFreshFor()
		t, err := time.Parse(time.RFC3339, h.Date)
		return fresh > 0 && err == nil && now.Sub(t) > fresh
	}
	return false
}

// --- the moves ledger --------------------------------------------------------

func recordMemoryMove(udb Database, m memoryMove, now time.Time) {
	m.ID, m.At = UUIDv4(), now
	udb.Set(memoryMovesTable, m.ID, m)
	Log("[orchestrate.memory.lifecycle] agent=%s %s: %q", m.AgentID, m.Kind, truncateObs(m.Note, 80))
}

// memoryMovesKept is how long a move can be undone: as long as the retired
// note it points at is kept.
func memoryMovesKept() time.Duration {
	days := TuneInt(TunableFactTombstoneDays)
	if days <= 0 {
		days = 30
	}
	return time.Duration(days) * 24 * time.Hour
}

func pruneMemoryMoves(udb Database, now time.Time) {
	for _, k := range udb.Keys(memoryMovesTable) {
		var m memoryMove
		if udb.Get(memoryMovesTable, k, &m) && now.Sub(m.At) > memoryMovesKept() {
			udb.Unset(memoryMovesTable, k)
		}
	}
}

// listMemoryMoves is one agent's recorded moves, newest first.
func listMemoryMoves(udb Database, agentID string) []memoryMove {
	var out []memoryMove
	for _, k := range udb.Keys(memoryMovesTable) {
		var m memoryMove
		if udb.Get(memoryMovesTable, k, &m) && m.AgentID == agentID && time.Since(m.At) <= memoryMovesKept() {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// undoMemoryMove puts a moved note back in the saved notes. A past event comes
// back as a standing note, so the next pass does not move it again, and the
// finding it became is removed. A closed open item comes back live, with its
// idle clock started over.
func undoMemoryMove(udb Database, agentID, moveID string) error {
	var m memoryMove
	if !udb.Get(memoryMovesTable, moveID, &m) || m.AgentID != agentID {
		return fmt.Errorf("no such move")
	}
	ns := factsNamespace(agentID)
	f, ok := GetMemoryFactByID(udb, ns, m.FactID)
	if !ok {
		udb.Unset(memoryMovesTable, moveID)
		return fmt.Errorf("the note is no longer kept, so it cannot be put back")
	}
	now := time.Now()
	f.Reason, f.RetiredAt, f.Successor, f.Updated = RetireLive, time.Time{}, "", now
	switch m.Kind {
	case "past_event":
		f.MemKind = provenance.MemKindFact
		if m.Finding != "" && VectorDB != nil {
			DeleteReportChunks(VectorDB, m.Finding)
		}
	case "not_pursued":
		f.AskedAt, f.AsOf = time.Time{}, now
	}
	udb.Set(MemoryFactsTable, ns+"/"+f.ID, f)
	udb.Unset(memoryMovesTable, moveID)
	Log("[orchestrate.memory.lifecycle] agent=%s undid %s: %q", agentID, m.Kind, truncateObs(m.Note, 80))
	return nil
}

// handleMemoryMovesPost serves the Memory panel's Undo.
func handleMemoryMovesPost(w http.ResponseWriter, r *http.Request, udb Database, agentID string) {
	var body struct {
		Undo string `json:"undo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Undo) == "" {
		http.Error(w, "undo is required", http.StatusBadRequest)
		return
	}
	if err := undoMemoryMove(udb, agentID, strings.TrimSpace(body.Undo)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// --- asking about an idle open item -----------------------------------------

// openItemAsks remembers, per session, the ask a turn was given, so every
// round of that turn carries the same note (the loop re-reads its notes each
// round) and the next turn does not ask about a second item on top.
var openItemAsks = struct {
	sync.Mutex
	m map[string]openItemAsk
}{m: map[string]openItemAsk{}}

type openItemAsk struct {
	msg, note string
	at        time.Time
}

// lastActive is the last time anything happened to a note.
func lastActive(f MemoryFact) time.Time {
	t := f.Created
	for _, c := range []time.Time{f.AsOf, f.Updated} {
		if c.After(t) {
			t = c
		}
	}
	return t
}

// openItemTurnNote picks the oldest idle open item nobody has been asked about
// yet, marks it asked, and returns the note that has the agent ask. At most
// one per turn, and only in a conversation that may touch durable memory.
func (t *chatTurn) openItemTurnNote(userMsg string) string {
	idle := openIdleFor()
	if t == nil || idle <= 0 || t.udb == nil || t.incognitoSession() {
		return ""
	}
	key := t.user + "\x00" + t.agent.ID + "\x00" + t.chatSessionID()
	openItemAsks.Lock()
	defer openItemAsks.Unlock()
	if a, ok := openItemAsks.m[key]; ok && a.msg == userMsg && time.Since(a.at) < time.Hour {
		return a.note
	}
	now := time.Now()
	ns := factsNamespace(t.agent.ID)
	var pick *MemoryFact
	for _, f := range ListMemoryFacts(t.udb, ns) {
		if f.MemKind != provenance.MemKindOpenItem || !f.AskedAt.IsZero() || now.Sub(lastActive(f)) <= idle {
			continue
		}
		if pick == nil || lastActive(f).Before(lastActive(*pick)) {
			f := f
			pick = &f
		}
	}
	if pick == nil {
		delete(openItemAsks.m, key)
		return ""
	}
	pick.AskedAt = now
	t.udb.Set(MemoryFactsTable, ns+"/"+pick.ID, *pick)
	id := "fact:" + pick.ID
	t.forgetOfferedMu.Lock()
	if t.forgetOffered == nil {
		t.forgetOffered = map[string]bool{}
	}
	t.forgetOffered[id] = true
	t.forgetOfferedMu.Unlock()
	days := int(now.Sub(lastActive(*pick)).Hours() / 24)
	note := frameworkNoteTag + fmt.Sprintf(
		"OPEN ITEM, idle %d days: your saved note %q (noted %s) has had no activity since %s. "+
			"After answering what the user asked, ask them once, in a sentence, whether they still want it. "+
			"If they do, save it again unchanged with %s, which marks it active. "+
			"If they don't, call forget(id=%q), which closes it as not pursued. "+
			"If they don't answer, leave it: it closes by itself in %d days. Do not ask about it again.",
		days, pick.Note, pick.Created.Format("2006-01-02"), lastActive(*pick).Format("2006-01-02"),
		memPinPhrase(), id, TuneInt(tuneOpenAnswerDays))
	openItemAsks.m[key] = openItemAsk{msg: userMsg, note: note, at: now}
	Log("[orchestrate.memory.lifecycle] agent=%s asking once about open item %s (idle %dd)", t.agent.ID, pick.ID, days)
	return note
}

// --- the daily run -------------------------------------------------------------

var memoryLifecycleLoop sync.Once

// startMemoryLifecycleLoop runs the pass once a day through the maintenance
// runner, so a run shows its progress and outcome on the admin panel like a
// pressed button does, and a press while it runs joins it.
func startMemoryLifecycleLoop() {
	memoryLifecycleLoop.Do(func() {
		go func() {
			// Let the rest of the app come up first.
			time.Sleep(5 * time.Minute)
			for {
				RunMaintenanceFunc(context.WithoutCancel(AppContext()), memoryLifecycleKey)
				time.Sleep(24 * time.Hour)
			}
		}()
	})
}
