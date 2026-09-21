// Per-session diagnostics trail — the framework's decisions made on the
// user's behalf inside one conversation: suppressed replies, discarded
// inputs, retries, reroutes. Guards that drop or rewrite content used to
// leave at best a server Debug line, which wiped exactly the evidence
// needed to diagnose "what went wrong" from the UI. Every guard now
// appends a bounded breadcrumb here; the chat panel's ⚠ affordance
// (ui.AgentLoopPanel.DiagnosticsURL) lists them per session. Cortex
// threads are sessions, so they get the same trail for free.

package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

const (
	sessionDiagTable = "session_diag"
	sessionDiagCap   = 50
)

// SessionDiag is one guard decision recorded against a session.
//
// Level and ID are DERIVED (diagLevel, diagID) rather than stored: both are
// readings of fields already in the record, so computing them on the way out
// keeps one source of truth and gives every entry already in a store the same
// reading as a new one.
type SessionDiag struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
	Level  string    `json:"level,omitempty"`
	ID     string    `json:"id,omitempty"`
}

// diagID names one breadcrumb the same way from both directions.
//
// It exists because a blocking breadcrumb reaches an open page twice by two
// honest routes: live on the run's event stream, and again from the trail
// when that page loads the session. A reload lands mid-run with BOTH — the
// run buffer replays from sequence zero — so without one identity the reader
// gets the same block told twice, and it is the kind of double that vanishes
// on the next reload and so never gets reported.
//
// Derived from the stamp, not stored: nanosecond time plus kind is unique per
// trail in any real sense, and derived means an entry written before this
// existed gets an id too.
func diagID(at time.Time, kind string) string {
	return at.UTC().Format(time.RFC3339Nano) + "|" + kind
}

// Levels a breadcrumb can carry. Only diagLevelBlocked reaches the
// conversation as it happens; everything else waits in the trail.
const (
	diagLevelBlocked = "blocked"
	diagLevelNote    = "note"
)

// diagBlockingVerbs are the words a guard uses when it STOPPED something —
// a tool that did not run, a draft that was not served, a turn cut short.
//
// Read off the kind slug rather than declared per call site, and that is the
// convention: name the kind for what the guard DID ("tool-denied",
// "guardrail-output-withheld") and it surfaces on the card for free. A kind
// naming a condition rather than an action ("guardrail-no-verdict",
// "skill_playbook_fired") stays in the trail, which is where a note nobody
// has to act on belongs. Sixty-eight call sites and counting: a hand-kept
// list of blocking kinds would be wrong within a release.
var diagBlockingVerbs = []string{"blocked", "denied", "withheld", "halted", "discarded", "refus"}

// diagProvisionalKinds are breadcrumbs whose verb says something was stopped
// when the framework may UNDO it inside the same turn.
//
// The verb rule reads what a guard DID, which is the right question for
// something that stays done. It cannot see whether it stuck, and that is the
// difference between the two kinds ending in "withheld": an output the warden
// refused is gone, while a lead-in is held on a bet about how the final reply
// will read and restored below it when the bet loses. One is a refusal; the
// other is a rendering decision with an automatic undo, and the text is in the
// model's history throughout either way.
//
// Told as a card it read "BLOCKED", in the same amber as a guardrail stopping
// a tool call, twice inside half a minute, for something nobody has to act on
// and that may reverse itself before they finish reading it. A kind with a
// named sibling for its own reversal (lead-in-restored) is the clearest case
// there is: it stays in the trail, where an explanation waits for whoever goes
// looking.
var diagProvisionalKinds = map[string]bool{
	"lead-in-withheld": true,
}

// diagByDesignKinds are breadcrumbs whose verb says something was stopped when
// nothing went wrong and the reader has nothing to do.
//
// A sibling of the list above, and a different reason for the same demotion.
// That one is "it may be undone before you finish reading"; this one is "it
// was never going to happen, and you knew". The verb rule cannot tell either
// from a guardrail refusing a tool call, because it reads what the guard DID
// rather than whether anybody has to act.
//
// authoring_withheld is the case. Running somebody else's agent means the
// authoring tools are absent, which is the correct and expected answer — the
// detail says "nothing is broken" in as many words — and it was showing an
// ordinary user an amber BLOCKED card, in their chat, about a permission they
// never asked for on an agent that is not theirs. It stays in the trail, where
// somebody debugging an absent tool_def will find it; it comes off the pane.
var diagByDesignKinds = map[string]bool{
	"authoring_withheld": true,
}

// diagLevel reads a kind slug and says how loudly it should be told.
func diagLevel(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if diagProvisionalKinds[k] || diagByDesignKinds[k] {
		return diagLevelNote
	}
	for _, verb := range diagBlockingVerbs {
		if strings.Contains(k, verb) {
			return diagLevelBlocked
		}
	}
	return diagLevelNote
}

// decorateSessionDiags fills in the derived display fields (Level, ID) on a
// trail. Used on the way out of every handler that serves one, so the reader
// and the live pane classify and identify by the same rules.
func decorateSessionDiags(list []SessionDiag) []SessionDiag {
	for i := range list {
		list[i].Level = diagLevel(list[i].Kind)
		list[i].ID = diagID(list[i].At, list[i].Kind)
	}
	return list
}

// appendSessionDiag records one guard decision. Stored in its own table
// (NOT on the ChatSession record) deliberately: a mid-turn write onto the
// session struct would race the turn's own end-of-turn save and one side
// would clobber the other. Bounded ring (last sessionDiagCap entries);
// best-effort — a diagnostics write must never fail a real operation.
func appendSessionDiag(udb Database, agentID, sessionID, kind, detail string) {
	appendSessionDiagAt(udb, agentID, sessionID, kind, detail, time.Now())
}

// appendSessionDiagAt is appendSessionDiag with the stamp supplied. A turn
// that also TELLS the open pane about a breadcrumb has to write both from one
// clock reading, because the stamp is half of the entry's identity (diagID)
// and two readings would be two entries as far as the page is concerned.
func appendSessionDiagAt(udb Database, agentID, sessionID, kind, detail string, at time.Time) {
	if udb == nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	key := agentID + ":" + sessionID
	var list []SessionDiag
	udb.Get(sessionDiagTable, key, &list)
	list = append(list, SessionDiag{At: at, Kind: kind, Detail: detail})
	if len(list) > sessionDiagCap {
		list = list[len(list)-sessionDiagCap:]
	}
	udb.Set(sessionDiagTable, key, list)
}

// beginDispatchDiag opens a DISPATCHED turn's diagnostics trail, and
// records the one thing a dispatch silently costs.
//
// The ids have to come from the caller: a dispatched turn has no
// *session of its own, and the record it should be filed against lives
// with whoever asked for the run. Every dispatch path wired these two
// fields by hand, which is also why this is the right place for the
// note below — a fourth path that wires diagnostics gets it for free,
// and one that does not was never going to record anything anyway.
//
// The note: a machine-driven agent dispatched (scheduled fire,
// delegation, phantom, sub-agent) runs WITHOUT its machine. That path
// assembles its own system prompt and there is no conversation to hold
// a position in, so there is no phase directive, no blackboard and no
// routing. It may be the right trade for a one-shot, but an agent whose
// whole procedure is its machine is not itself here, and a difference
// that large must not be silent.
func (t *chatTurn) beginDispatchDiag(agentID, sessionID string) {
	if t == nil {
		return
	}
	t.diagAgentID = agentID
	t.diagSessionID = sessionID
	m := strings.TrimSpace(t.agent.Machine)
	if m == "" {
		return
	}
	// The machine belongs to whoever authored the agent, not to the
	// runtime user a dispatch happens to run as (a phantom chat, a
	// scheduled job's account).
	db, owner := t.ownerDB, t.ownerUser
	if db == nil {
		db, owner = t.udb, t.user
	}
	name := m
	if def, ok := LoadMachineDef(db, owner, m); ok && strings.TrimSpace(def.Name) != "" {
		name = def.Name
	}
	t.turnDiag("machine_not_on_dispatch", "this agent runs the machine "+strconv.Quote(name)+
		", which needs a conversation to hold its position; a dispatched turn has none, so this one ran on the agent's persona alone")
}

// diagParentKey carries the CONVERSATION a dispatched turn descends from —
// the trail a person can actually open.
//
// A dispatched sub-agent files its breadcrumbs under its own sub-session, and
// the chat page cannot open a sub-session (runOwnerDestination says why), so
// every guardrail halt, denied tool and machine_not_on_dispatch on a delegated
// run was written and never readable. The live turn stamps its own
// (agent, session) on its context before any tool runs; a dispatch inherits
// the context, and turnDiag on a turn with no *session of its own MIRRORS
// each breadcrumb into that conversation's trail, tagged with the sub-agent's
// name. Only the live turn stamps — a sub-turn never re-stamps with its own
// sub-session — so any depth of nesting lands in the one thread the owner
// is looking at.
type diagParentKey struct{}

type diagParent struct{ agentID, sessionID string }

// withDiagParent names the conversation later breadcrumbs mirror into.
func withDiagParent(ctx context.Context, agentID, sessionID string) context.Context {
	if ctx == nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" {
		return ctx
	}
	return context.WithValue(ctx, diagParentKey{}, diagParent{agentID: agentID, sessionID: sessionID})
}

// diagParentFrom reads the stamp, or ok=false at the top of a conversation.
func diagParentFrom(ctx context.Context) (agentID, sessionID string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	p, ok := ctx.Value(diagParentKey{}).(diagParent)
	return p.agentID, p.sessionID, ok
}

// diagNoticeKey carries the OPEN CONVERSATION PANE a breadcrumb should also
// be told to, live.
//
// Separate from diagParentKey on purpose, because they answer different
// questions: the parent stamp says which trail a dispatched turn's breadcrumb
// is filed under, and this one says which stream is being watched right now.
// A background fire has the first and not the second; a turn whose reader
// closed the tab has the first and a sink that writes only to the run buffer.
//
// The sub-turn needs the stamp for the same reason the trail does: a
// delegated run builds its own chatTurn with a nil sse (agent_dispatch.go),
// so a guardrail that stopped a sub-agent mid-delegation had nowhere live to
// say so, and the pane showed the tool chip sitting there.
type diagNoticeKey struct{}

// withDiagNotices names the stream live breadcrumbs are mirrored to.
func withDiagNotices(ctx context.Context, sink *sseWriter) context.Context {
	if ctx == nil || sink == nil {
		return ctx
	}
	return context.WithValue(ctx, diagNoticeKey{}, sink)
}

// diagNoticeSinkFrom reads the stamp, or nil when nobody is watching.
func diagNoticeSinkFrom(ctx context.Context) *sseWriter {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(diagNoticeKey{}).(*sseWriter)
	return sink
}

// emitDiagNotice tells the open conversation, as it happens, that a guard
// stopped something.
//
// The trail alone was not enough. A block lands in the middle of a turn the
// user is watching: a tool chip that never resolves, a reply that arrives
// shorter than it should have, or nothing at all — and the only record of
// why sat behind the ⚠ button, which a person has no reason to press unless
// they already suspect a guard fired. So the breadcrumb is written where the
// thing happened, in the conversation flow, at the moment it happened.
//
// Only diagLevelBlocked. Every guard leaves a breadcrumb (the house rule),
// but most of them record a condition nobody has to act on, and a card per
// condition would teach the reader to stop reading the cards.
func (t *chatTurn) emitDiagNotice(kind, detail string, at time.Time) {
	if t == nil || diagLevel(kind) != diagLevelBlocked {
		return
	}
	// A live turn owns the pane. A dispatched sub-turn has no sse of its own
	// and borrows the one it descends from, naming itself the way the trail
	// mirror does — otherwise "blocked a pre_action check" in the middle of a
	// delegation reads as the agent the user is talking to.
	sink, prefix := t.sse, ""
	if sink == nil {
		if sink = diagNoticeSinkFrom(t.ctx); sink == nil {
			return
		}
		name := strings.TrimSpace(t.agent.Name)
		if name == "" {
			name = t.agent.ID
		}
		prefix = "↳ " + name + ": "
	}
	sink.Send(map[string]any{
		"kind":  "notice",
		"level": diagLevelBlocked,
		"type":  kind,
		"text":  prefix + detail,
		// The same name the trail will serve for this entry, so a page that
		// receives it both ways shows it once. See diagID.
		"id": diagID(at, kind),
		"at": at.UTC().Format(time.RFC3339Nano),
	})
}

// redactGuardrailDetail rewrites a guardrail diagnostic when the person
// reading the turn is not the person who wrote the rule.
//
// Two things have to be true at once and the unredacted text only manages one.
// The reader must know something was WITHHELD: an agent that fetches a joke and
// then declines to tell it, with nothing else on screen, reads as broken, and
// they re-ask in circles or stop using it. And they must not be handed the rule
// itself — its text, the hook it fired at, the warden's reason. That is the
// owner's policy, and on a shared agent it is also the exact shape to phrase
// around.
//
// So the card stays and its contents go. The owner is told in full through the
// block log and a notice (see guardrail_log.go); this is the other half of that
// split, and it is HERE rather than at the ~15 call sites because the trail is
// persisted in the reader's own store and a call site added later would
// otherwise write the rule into it.
func (t *chatTurn) redactGuardrailDetail(kind, detail string) string {
	if t == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(kind)), "guardrail") {
		return detail
	}
	by := t.ranBy()
	if by == "" {
		return detail
	}
	who := strings.TrimSpace(t.ownerUser)
	if who == "" {
		who = "the owner of this agent"
	}
	// Says the omission is deliberate. "A rule stopped this" on its own invites
	// the reader to hunt for the missing half and conclude the message is
	// broken too.
	return "A rule set by " + who + " stopped this. They have been told it stopped you. Which rule, and why, is theirs to see."
}

// turnDiag is appendSessionDiag bound to a chatTurn — the convenient form
// for guards firing inside a live turn. Nil-safe on every field.
func (t *chatTurn) turnDiag(kind, detail string) {
	if t == nil {
		return
	}
	detail = t.redactGuardrailDetail(kind, detail)
	// A diagnostic is text a PERSON reads, so it goes out through the same
	// delivery scrub as a reply. Two reasons it belongs here rather than in
	// each of the ~60 call sites: the framework writes these strings itself and
	// wrote em-dashes into a dozen of them, and a detail routinely quotes the
	// model (a refusal, a withheld output), which can carry a marker of its
	// own. One funnel, so a diag added later cannot miss it.
	detail = prompts.ApplyRuleEnforcers(StripMetaTags(detail))
	// ONE clock reading for all three destinations — the open pane, this
	// turn's trail, and the parent's — because the stamp is half of what
	// names the entry (diagID), and three readings would be three entries as
	// far as the page is concerned.
	at := time.Now()
	// The pane hears about it first. A live notice is only worth anything
	// while the reader is still looking at the turn it belongs to, and the
	// store write is the one part of this that can be slow.
	t.emitDiagNotice(kind, detail, at)
	defer t.mirrorDiagToParent(kind, detail, at)
	// A live turn writes to its own session. A BACKGROUND turn (scheduled fire,
	// monitor wake, dispatched sub-agent) has no *session at all — it was built
	// for the run, and the session record lives with the caller. Those turns run
	// the same guards, so requiring a *session silently discarded every
	// breadcrumb they left: the guardrail that stopped a 3am fire was in the
	// server log and nowhere a user would ever look.
	agentID, sessionID := t.agent.ID, ""
	if t.session != nil {
		sessionID = t.session.ID
	} else {
		if t.diagAgentID != "" {
			agentID = t.diagAgentID
		}
		sessionID = t.diagSessionID
	}
	if sessionID == "" {
		return // genuinely no trail to write to
	}
	// Write to the OWNER's store, not the runtime user's. A breadcrumb exists
	// for the person who configured the agent, and the trail is read back
	// through the requesting user's own store (handleSessionDiag) — so a turn
	// running as a synthetic per-chat identity that filed its diagnostics under
	// that identity filed them where nobody can ever look. Falls back to the
	// turn's own store for the ordinary case, where the two are the same.
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	appendSessionDiagAt(db, agentID, sessionID, kind, detail, at)
}

// mirrorDiagToParent copies a dispatched turn's breadcrumb into the
// conversation it descends from (see diagParentKey), tagged with this agent's
// name so the reader knows which child left it. A live turn — one with its
// own *session — is its own conversation and mirrors nothing, and a stamp
// that names this very trail (a dispatch that happens to file under the
// parent's ids) is not written twice.
func (t *chatTurn) mirrorDiagToParent(kind, detail string, at time.Time) {
	if t == nil || t.session != nil {
		return
	}
	pAgent, pSession, ok := diagParentFrom(t.ctx)
	if !ok {
		return
	}
	ownAgent, ownSession := t.agent.ID, t.diagSessionID
	if t.diagAgentID != "" {
		ownAgent = t.diagAgentID
	}
	if pAgent == ownAgent && pSession == ownSession {
		return
	}
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	name := strings.TrimSpace(t.agent.Name)
	if name == "" {
		name = t.agent.ID
	}
	appendSessionDiagAt(db, pAgent, pSession, kind, "↳ "+name+": "+detail, at)
}

// handleSessionDiag serves the trail: GET /api/session-diag?agent=&session=
// → [{at, kind, detail}], newest first. Scoped to the requesting user's own
// store, so one user can never read another's trail.
func (T *OrchestrateApp) handleSessionDiag(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if agent == "" || session == "" {
		http.Error(w, "agent and session required", http.StatusBadRequest)
		return
	}
	var list []SessionDiag
	udb.Get(sessionDiagTable, agent+":"+session, &list)
	list = decorateSessionDiags(list)
	// Newest first for display.
	out := make([]SessionDiag, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	writeJSON(w, out)
}

// PublicHandleSessionDiag is the landing an app routes its AgentLoopPanel's
// DiagnosticsURL to. The agent id is the caller's, so an app cannot read
// another agent's trail by asking for it in the query string — which the
// admin-mounted variant can, because there the caller IS the operator.
//
// This is the answer to "why did that stop". The entries are the framework's
// decisions taken on the user's behalf inside one conversation — a denied
// tool, an approval that timed out, a guardrail that dropped a call — and
// without a surface they exist only in the server log, which is not where the
// person who asked the question is looking.
func (T *OrchestrateApp) PublicHandleSessionDiag(w http.ResponseWriter, r *http.Request, agentID, sessionID string) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if agentID == "" || sessionID == "" {
		http.Error(w, "agent and session required", http.StatusBadRequest)
		return
	}
	var list []SessionDiag
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	list = decorateSessionDiags(list)
	out := make([]SessionDiag, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// PublicHandleSessionRename renames one of the caller's sessions. Body is
// {id, name}, which is the shape core/ui's rename affordance POSTs — the id
// travels in the body rather than the path, so the URL needs no template.
//
// A coding thread's auto-title comes from its first message, which is
// routinely the least descriptive thing about it ("have a look at this").
func (T *OrchestrateApp) PublicHandleSessionRename(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || strings.TrimSpace(body.ID) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	renameChatSession(udb, agentID, body.ID, body.Name)
	w.WriteHeader(http.StatusNoContent)
}
