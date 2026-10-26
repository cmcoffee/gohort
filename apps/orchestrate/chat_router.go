// Flat API surface for the agent-dropdown-driven chat page. The active
// agent rides on every request via the `agent_id` query string (GET /
// DELETE) or JSON body field (POST). Authorization always checks
// owner — users can't reach into another user's agent buckets by
// guessing IDs, and the seed pseudo-owner ("system") is read-allowed
// for everybody.

package orchestrate

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/tools/temptool"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// resolveAgent loads the agent named by `agent_id` (query first, then
// JSON body for POSTs) and authorizes it against the user. Writes the
// appropriate HTTP error and returns ok=false on any failure so
// callers can early-return.
func (T *OrchestrateApp) resolveAgent(w http.ResponseWriter, r *http.Request, udb Database, user string) (AgentRecord, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	// POST bodies — we need to peek without consuming the stream the
	// real handler will read. Cap the peek so a giant body can't OOM
	// us. The handlers that read the body re-decode from r.Body, so
	// we restore it via a NopCloser after the peek.
	if id == "" && r.Method == http.MethodPost {
		// Try the body. Reset it after.
		var head struct {
			AgentID string `json:"agent_id"`
		}
		raw, err := readAndRestoreBody(r)
		if err == ErrBodyTooLarge {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return AgentRecord{}, false
		}
		if err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &head)
			id = strings.TrimSpace(head.AgentID)
		}
	}
	if id == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return AgentRecord{}, false
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return AgentRecord{}, false
	}
	return agent, true
}

// sessionListItem is the wire shape returned by handleSessionList.
// Embeds ChatSession for the native rows; the optional Source +
// ChatID fields carry tagging contributed by external sources (see
// core/session_sources.go — phantom uses these so its dispatched
// sessions surface in Agency's rail with a per-chat badge).
//
// Running marks a session as currently mid-turn — i.e. the runs
// registry has an in-flight Run for this session ID. The rail uses
// this to render an "active" indicator so the user can see, at a
// glance, which sessions are still working after a disconnect /
// reconnect. Computed at request time from the run registry, not
// persisted on the session record.
type sessionListItem struct {
	ChatSession
	Source string `json:"source,omitempty"`
	ChatID string `json:"chat_id,omitempty"`
	// ChannelID, when set, marks this row as a messaging-channel binding (the
	// Channels rail group). The rail's delete (×) then removes the binding via
	// the channel-delete URL instead of only wiping the session transcript.
	ChannelID string `json:"channel_id,omitempty"`
	Running   bool   `json:"running,omitempty"`
	// Watchers / Dispatches surface LIVE background work attached to a
	// session — distinct from Running (this session mid-turn) and Unread
	// (a past append). Watchers = enabled event-monitors whose wake lands
	// here; Dispatches = in-flight dispatched sub-agents hosted here. Both
	// derived at request time, not persisted. The rail uses them for an
	// "active" badge so sessions with ongoing work are findable.
	Watchers   int `json:"watchers,omitempty"`
	Dispatches int `json:"dispatches,omitempty"`
}

// handleSessionList returns the chat sessions for the active agent.
// Returns an empty list (not 400) when agent_id is blank so the
// AgentLoopPanel's first fetch — before the user picks an agent —
// renders cleanly instead of as an error toast.
//
// The response merges native sessions (this user's DB) with rows
// contributed by any registered ExtraSessionsSource (phantom today).
// External rows carry source + chat_id tags so the rail can render
// per-source visual markers.
func (T *OrchestrateApp) handleSessionList(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	id := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if id == "" {
		return []sessionListItem{}, nil
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		return []sessionListItem{}, nil
	}
	native := listChatSessions(udb, agent.ID)
	out := make([]sessionListItem, 0, len(native))
	runs := T.runsRegistry()

	// Derived "active background work", bucketed by session id once (session
	// ids are UUIDs, so matching by id is already user-scoped). Enabled
	// event-monitors that wake a session, and in-flight dispatched sub-agents
	// hosted by a session, each make that session "active" in the rail.
	watcherCounts := map[string]int{}
	for _, m := range ListEventMonitors(RootDB, user) {
		if m.Paused {
			continue
		}
		// Counted where the monitor surfaces: its agent's cortex.
		if sess := scheduleSession(m.WakeAgent, m.WakeSession); sess != "" {
			watcherCounts[sess]++
		}
	}
	dispatchCounts := map[string]int{}
	for _, k := range RootDB.Keys(SubSessionsTable) {
		var ss SubSession
		if RootDB.Get(SubSessionsTable, k, &ss) && ss.Status == SubSessionActive && ss.HostSessionID != "" {
			dispatchCounts[ss.HostSessionID]++
		}
	}

	for _, s := range native {
		item := sessionListItem{ChatSession: s}
		// Running flag: in-flight Run keyed by session ID. BySession
		// returns nil for unknown / no-run-active. Cheap map lookup
		// per session — fine for typical list sizes.
		if r := runs.BySession(user, s.ID); r != nil && r.Status() == RunStatusRunning {
			item.Running = true
		}
		item.Watchers = watcherCounts[s.ID]
		item.Dispatches = dispatchCounts[s.ID]
		out = append(out, item)
	}
	for _, ext := range CollectExtraSessions(T.DB, agent.ID, user) {
		item := sessionListItem{
			ChatSession: ChatSession{
				ID:      ext.ID,
				AgentID: ext.AgentID,
				Title:   ext.Title,
				LastAt:  ext.LastAt,
			},
			Source: ext.Source,
			ChatID: ext.ChatID,
		}
		if r := runs.BySession(user, ext.ID); r != nil && r.Status() == RunStatusRunning {
			item.Running = true
		}
		item.Watchers = watcherCounts[ext.ID]
		item.Dispatches = dispatchCounts[ext.ID]
		out = append(out, item)
	}
	// Channel bindings render in their OWN rail region (ChannelsURL), not in
	// this session list — so the list stays chat-sessions only. The client
	// filters out chan: rows defensively.
	return out, nil
}

// handleSessionOne loads or deletes a session in the active agent's
// bucket. agent_id is required on both methods.
// servedSession is a ChatSession on its way OUT, plus how many messages were
// left off the front.
//
// A serving-only wrapper rather than a field on ChatSession, which is the
// stored struct: an offset is a fact about one response, not about the thread,
// and persisting it would be a number that is wrong the moment anything else
// writes.
//
// MessageOffset exists BEFORE anything trims, deliberately. The client computes
// a message's storage index from its position in the array it received, and
// uses that index to scrub or truncate — so the day a tail is served, every one
// of those indices is silently off by the number dropped, and the scrub deletes
// the wrong message. Shipping the offset first, while it is always 0, makes
// that a no-op change; shipping the tail first makes it data loss.
type servedSession struct {
	ChatSession
	MessageOffset int `json:"message_offset"`
	// OpeningPrompt is served only while the session is still EMPTY. The
	// panel sends it as the first user message, and once that lands the
	// session has messages and this stops being served — which is the
	// whole idempotency guard. No extra state, no window where a reload
	// sends it twice.
	OpeningPrompt string `json:"opening_prompt,omitempty"`
	// ChunkSize is how many messages one "show earlier" press should add.
	//
	// Served rather than assumed by the client, because the size depends on
	// which thread this is — a cortex is bounded tighter than an ordinary
	// conversation — and a client guessing it would either re-render more than
	// the server thinks a chunk is, or creep back through a long thread a
	// handful of messages at a time.
	ChunkSize int `json:"chunk_size,omitempty"`
}

func (T *OrchestrateApp) registerSessionRoutes() {
	T.HandleFunc("/api/sessions/mark-all-read", jsonapi.Post(T.DB, T.sessionsMarkAllRead))
	T.HandleFunc("/api/sessions/rename", jsonapi.Post(T.DB, T.sessionRename))
	T.HandleFunc("/api/sessions/{sid}", T.sessionRoute(T.ownSession))
	T.HandleFunc("/api/sessions/{sid}/continue", jsonapi.Post(T.DB, T.sessionContinue))
	T.HandleFunc("/api/sessions/{sid}/send-to-builder", jsonapi.Post(T.DB, T.sessionSendToBuilder, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/sessions/{sid}/blocks/{block}/resolve", T.blockResolveRoute(func(c *jsonapi.Call) (string, string, string, error) {
		agent, err := T.sessionAgent(c)
		return agent.ID, c.R.PathValue("sid"), c.R.PathValue("block"), err
	}))
	T.HandleFunc("/api/sessions/{sid}/tools", jsonapi.Get(T.DB, T.sessionTools))
	T.HandleFunc("/api/sessions/{sid}/tools/{name}", jsonapi.Post(T.DB, T.sessionToolAction))
	// A download: the full session as JSON or markdown, for sharing and
	// debugging.
	T.HandleFunc("/api/sessions/{sid}/export", T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, udb Database) {
		agent, ok := T.resolveAgent(w, r, udb, user)
		if !ok {
			return
		}
		T.handleSessionExport(w, r, agent.ID, r.PathValue("sid"))
	}))
}

// sessionAgent is the agent ?agent_id= names, the caller's own or a seed: a
// session belongs to an agent, and a caller reaches only their own agent's.
func (T *OrchestrateApp) sessionAgent(c *jsonapi.Call) (AgentRecord, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("agent_id"))
	if id == "" {
		return AgentRecord{}, jsonapi.BadRequest("agent_id required")
	}
	agent, ok := loadAgent(c.UDB, id)
	if !ok || (agent.Owner != c.User && agent.Owner != seedOwner) {
		return AgentRecord{}, errNotFound
	}
	return agent, nil
}

// sessionAt is the agent and session a call is about.
type sessionAt func(c *jsonapi.Call) (agent AgentRecord, sid string, err error)

// ownSession is the session the path names, of the agent ?agent_id= names.
func (T *OrchestrateApp) ownSession(c *jsonapi.Call) (AgentRecord, string, error) {
	agent, err := T.sessionAgent(c)
	return agent, c.R.PathValue("sid"), err
}

// sessionRoute is one session: GET reads it, DELETE removes it, PATCH
// truncates it. Orchestrate serves it for the path's session; the agents app,
// customapps and filestore serve it, through PublicHandleSessionOne, for the
// session of the agent they have already let the caller reach.
func (T *OrchestrateApp) sessionRoute(at sessionAt) http.HandlerFunc {
	return jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) { return T.sessionGet(c, at) }),
		"DELETE": jsonapi.Delete(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
			return T.sessionDelete(c, at)
		}),
		"PATCH": jsonapi.Patch(T.DB, func(c *jsonapi.Call, body sessionCut) (any, error) {
			return T.sessionTruncate(c, at, body)
		}),
	}.Serve
}

// sessionsMarkAllRead clears the unread state on EVERY session of the agent
// (the sidebar "Mark all read" action).
func (T *OrchestrateApp) sessionsMarkAllRead(c *jsonapi.Call, _ struct{}) (any, error) {
	agent, err := T.sessionAgent(c)
	if err != nil {
		return nil, err
	}
	markAllSessionsSeen(c.UDB, agent.ID)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// sessionRename labels a thread, e.g. "Phantom Monitor", so the sessions where
// monitor wakes land are identifiable in the rail. The id is in the body, not
// the path, since core/ui's rename affordance POSTs it that way.
func (T *OrchestrateApp) sessionRename(c *jsonapi.Call, in sessionName) (any, error) {
	agent, err := T.sessionAgent(c)
	if err != nil {
		return nil, err
	}
	return renameSession(c, agent.ID, in)
}

// sessionName is a rename: which session, and what to call it.
type sessionName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// renameSession renames one of the caller's sessions with agentID.
func renameSession(c *jsonapi.Call, agentID string, in sessionName) (any, error) {
	if strings.TrimSpace(in.ID) == "" {
		return nil, jsonapi.BadRequest("bad request")
	}
	renameChatSession(c.UDB, agentID, in.ID, in.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// sessionGet serves one session: its tail, with the cards still awaiting a
// decision, and with ?cards=1 only the cards the page has not seen.
//
// Channel agents no longer force every visit onto one thread — they have a
// channel thread AND normal sessions. The channel thread is just a specific
// session id (cortexSessionID) the client opens via the Channel row.
func (T *OrchestrateApp) sessionGet(c *jsonapi.Call, at sessionAt) (any, error) {
	agent, sid, err := at(c)
	if err != nil {
		return nil, err
	}
	r, user, udb := c.R, c.User, c.UDB
	// External-source row (e.g. ?source=phantom&chat_id=…)?
	// Delegate to the registered source so it can resolve to
	// the right per-source storage scope. Sources gate user
	// permission internally; on a false result we serve the
	// same 404 native misses get, so a leaked source name
	// can't enumerate other users' rows.
	if src := strings.TrimSpace(r.URL.Query().Get("source")); src != "" {
		if source, found := LookupExtraSessionsSource(src); found {
			if payload, ok := source.LoadSession(T.DB, agent.ID, user, sid, r.URL.Query().Get("chat_id")); ok {
				return payload, nil
			}
		}
		return nil, errNotFound
	}
	s, ok := loadChatSession(udb, agent.ID, sid)
	if !ok {
		// The channel thread doesn't exist until its first turn; when the
		// Channel row opens it, return an empty session instead of 404 so
		// the client opens a fresh thread rather than erroring. Ad-hoc
		// sessions still 404 on a miss.
		if agent.Cortex && sid == cortexSessionID(agent.ID) {
			s = ChatSession{ID: sid}
		} else {
			return nil, errNotFound
		}
	}
	// The live card poll, which wants only what it hasn't seen. Answered
	// before everything below: it takes no read receipt (a timer firing is
	// not a person reading), computes no blocks, and serializes no chat
	// turns. See session_cards.go for why that matters on a long thread.
	if r.URL.Query().Get("cards") == "1" {
		since := r.URL.Query().Get("since")
		// The run working for this thread in the background (a task's
		// wake), so an open page shows it working, with Stop.
		bg := func() *Run { return T.runsRegistry().BackgroundFor(user, sid) }
		if r.URL.Query().Get("wait") == "1" {
			s = waitForCards(r.Context(), udb, agent.ID, sid, s, since, r.URL.Query().Get("bg"), bg)
		}
		return observationCards(s.Messages, since, bg()), nil
	}
	// Opening a session clears its unread state (a background wake that
	// landed here is now seen). Writes LastSeen only — not activity.
	markSessionSeen(udb, agent.ID, sid)
	// Anything still awaiting the owner's decision rides along as a card in
	// the thread, so a run that stalled unattended says so where the user is
	// rather than only as a count on the Permissions tile. Computed per load
	// and NOT persisted — resolving one anywhere drops it from the next load,
	// with no stale card to sweep.
	// An actionable card the user already answered must not come back
	// asking again. Cards answered IN the chat carry a stored Resolved
	// stamp; cards whose subject was settled elsewhere (the admin typed
	// the secret in Admin > APIs) get theirs derived here, on the copy
	// being served — the stored record is left alone so a re-drafted
	// credential gets a live card again.
	s.UIBlocks = settleResolvedBlocks(s.UIBlocks, user)
	// A card that VIEWS live permissions must not replay an authoring-time
	// snapshot of them (see refreshPrivilegeBlocks).
	s.UIBlocks = refreshPrivilegeBlocks(udb, s.UIBlocks)
	s.UIBlocks = append(s.UIBlocks, pendingApprovalBlocks(udb, user, agent.ID)...)
	// Only the tail is rendered. Blocks and plans are session-level and
	// ride along whole — they are not indexed by message, so trimming
	// messages cannot orphan one.
	markFlaggedReplies(T.DB, user, agent.ID, sid, s.Messages)
	hideFrameworkNotes(s.Messages)
	var off int
	isCortex := sid == cortexSessionID(agent.ID)
	s.Messages, off = tailMessages(s.Messages, resolveTailLimitFor(r.URL.Query().Get("limit"), isCortex))
	markFoldPoint(loadCompactState(udb, agent.ID, sid), s.Messages, off)
	// The chunk is this thread's OWN default, not whatever the client just
	// asked for: after a "show earlier" the request carries an accumulated
	// limit, and echoing that back would make every press add as much as
	// everything already loaded — the doubling this replaced.
	out := servedSession{ChatSession: s, MessageOffset: off, ChunkSize: resolveTailLimitFor("", isCortex)}
	if len(s.Messages) == 0 {
		out.OpeningPrompt = s.OpeningPrompt
	}
	return out, nil
}

// sessionDelete removes the session, tearing down any persistent shells it
// opened (psql/redis-cli/ssh/etc.) so the bwrap processes don't leak. Scope
// is sess.ChatSessionID — matches what the persistent_shell substrate uses as
// its registry key prefix.
func (T *OrchestrateApp) sessionDelete(c *jsonapi.Call, at sessionAt) (any, error) {
	agent, sid, err := at(c)
	if err != nil {
		return nil, err
	}
	temptool.TerminateSessionShellsByScope(sid)
	deleteChatSession(c.UDB, agent.ID, sid)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// sessionCut is a truncate: where to cut, or the one message to scrub.
type sessionCut struct {
	At int `json:"at"`
	// DeleteAt (pointer = presence) scrubs EXACTLY ONE message at this
	// raw storage index, keeping everything after it — the in-thread
	// per-turn delete affordance. Distinct from At, which truncates from
	// the index onward. Raw index (client carries it from the load
	// payload, which includes hidden messages), so it lands precisely.
	DeleteAt *int `json:"delete_at"`
}

// sessionTruncate is PATCH: {at: N} drops messages from index N onward, and
// {delete_at: N} scrubs exactly one. Used by the per-message Edit and Delete
// affordances: the client drops the DOM rows, this drops the stored ones, and
// a follow-up send (on Edit) appends a fresh exchange below the cut.
func (T *OrchestrateApp) sessionTruncate(c *jsonapi.Call, at sessionAt, body sessionCut) (any, error) {
	agent, sid, err := at(c)
	if err != nil {
		return nil, err
	}
	udb := c.UDB
	s, ok := loadChatSession(udb, agent.ID, sid)
	if !ok {
		return nil, errNotFound
	}
	if body.DeleteAt != nil {
		di := *body.DeleteAt
		if di >= 0 && di < len(s.Messages) {
			s.Messages = append(s.Messages[:di], s.Messages[di+1:]...)
			if _, err := saveChatSession(udb, s); err != nil {
				return nil, jsonapi.Status(http.StatusInternalServerError, "save: "+err.Error())
			}
			// Keep the rolling-summary cursor aligned when scrubbing a turn
			// already folded into the summary: when the cursor reaches zero
			// the digest no longer reflects the thread, so clear it too.
			if st := loadCompactState(udb, agent.ID, sid); di < st.SummarizedThrough {
				st.SummarizedThrough--
				if st.SummarizedThrough <= 0 {
					st.SummarizedThrough = 0
					st.Summary = ""
				}
				saveCompactState(udb, agent.ID, sid, st)
			}
		}
		return map[string]any{"deleted_at": body.DeleteAt, "messages_remaining": len(s.Messages)}, nil
	}
	if body.At < 0 {
		body.At = 0
	}
	if body.At > len(s.Messages) {
		body.At = len(s.Messages)
	}
	// What the cut turns recorded about the tools they edited goes with
	// them, or the build check holds the next turn to it.
	if body.At < len(s.Messages) {
		forgetToolVerificationsSince(udb, sid, s.Messages[body.At].Created)
	}
	s.Messages = s.Messages[:body.At]
	if _, err := saveChatSession(udb, s); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "save: "+err.Error())
	}
	// Truncating the whole thread away (At==0) must also drop the rolling
	// summary — otherwise the orchestrator's compaction digest survives the
	// clear and re-primes every turn (the "stuck personality" trap). No-op
	// for agents with no compact-state row.
	if body.At == 0 {
		deleteCompactState(udb, agent.ID, sid)
	}
	return map[string]any{"at": body.At, "messages_remaining": len(s.Messages)}, nil
}

// handleSendRouter is the flat-route wrapper around the runner. Reads
// agent_id from the body, loads + authorizes the agent, then hands
// off to the (unchanged) per-agent runner.
func (T *OrchestrateApp) handleSendRouter(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := T.resolveAgent(w, r, udb, user)
	if !ok {
		return
	}
	T.handleSend(w, r, udb, user, agent)
}

// cancelOwnTurn is cancelTurn for orchestrate's own route, where the request
// can name an agent (?agent_id=, or agent_id in the body). One named has to be
// the caller's own or a seed; none named is fine, since the cancel is keyed by
// the caller and the session alone. The Agency chat panel's cancel POSTs only
// ?id=<session>, and hard-requiring agent_id once 400'd it before the run
// was reached: the button aborted the client stream, but the run loop
// (detached from the HTTP request for EVERY session) kept going.
func (T *OrchestrateApp) cancelOwnTurn(c *jsonapi.Call, in cancelTarget) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("agent_id"))
	if id == "" {
		id = strings.TrimSpace(in.AgentID)
	}
	if id != "" {
		agent, ok := loadAgent(c.UDB, id)
		if !ok || (agent.Owner != c.User && agent.Owner != seedOwner) {
			return nil, errNotFound
		}
	}
	return T.cancelTurn(c, in)
}

// ErrBodyTooLarge means the request exceeded what the peek will buffer. It is
// returned INSTEAD of a truncated body: the whole point of this helper is that
// the real handler decodes what it restores, so handing back a short read is
// handing back corrupt JSON.
var ErrBodyTooLarge = Error("request body too large")

// readAndRestoreBody slurps r.Body into memory and replaces r.Body
// with a fresh reader over the same bytes so the downstream handler
// can read it normally. Used by resolveAgent to peek the JSON body
// for agent_id without consuming the stream.
//
// Cap at 1MB — orchestrate POST bodies are small (a user message
// plus a few flags); anything larger is suspect and shouldn't be
// silently buffered.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	// The cap has to clear the largest LEGITIMATE body, because this restores
	// what it read. At 1 MiB it did not: a chat send carries its attachments
	// base64-encoded, which is about 1.37x the file, so any real photo cut the
	// JSON mid-string and the handler decoded a truncated request — the
	// attachment simply was not in it. Read one byte past the cap so a body AT
	// the limit is still served, and refuse rather than truncate beyond it.
	const max = 64 << 20
	buf, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(buf) > max {
		return nil, ErrBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}
