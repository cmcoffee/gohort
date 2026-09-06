package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleSend drives one user turn against an agent:
//
//  1. Append the user message to the session.
//  2. Orchestrator round 1: thinking LLM with the plan_set tool.
//     Returns a list of step titles.
//  3. Emit a plan block + one activity row per step.
//  4. For each step: call the worker LLM, stream its output to the
//     activity pane, flip the step status to done.
//  5. Orchestrator synthesis round: thinking LLM composes a final
//     user-facing reply given the original question + all worker
//     outputs. Stream as chunk events into the conversation pane.
//  6. Save the session (messages + plan snapshot) and emit done.
//
// Plans auto-confirm — no operator pause between steps in Phase 1.
func (T *OrchestrateApp) handleSend(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord) {
	T.handleSendWithAppTools(w, r, udb, user, agent, nil)
}

// handleSendWithAppTools is handleSend plus extra host-app tools injected into
// the agent's catalog for this run (see chatTurn.appTools). The plain handleSend
// passes nil.
// handleSendWithAppTools is the publisher-free spelling, for the apps whose
// tools only return text.
func (T *OrchestrateApp) handleSendWithAppTools(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord, appTools []AgentToolDef) {
	T.handleSendWithAppToolsPublishing(w, r, udb, user, agent, appTools, nil)
}

// handleSendWithAppToolsPublishing additionally binds a host app's block
// publisher to this turn, so an app tool can put a card in the conversation.
// See app_block_publisher.go for why the binding happens here rather than at
// tool-build time.
func (T *OrchestrateApp) handleSendWithAppToolsPublishing(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord, appTools []AgentToolDef, appBlockPub *AppBlockPublisher) {
	// Before anything else: a clock started after the first phase cannot
	// measure it, and the phases that turned out to be slow are near the front.
	prep := startPrepClock()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message          string        `json:"message"`
		SessionID        string        `json:"session_id,omitempty"`
		History          []ChatMessage `json:"history,omitempty"`
		PrivateMode      bool          `json:"private_mode,omitempty"`
		InferredDisabled bool          `json:"inferred_disabled,omitempty"`
		// Incognito (clean-room session) — honored only on the FIRST turn, when
		// the session is created; afterwards the stored session flag governs.
		Incognito bool `json:"incognito,omitempty"`
		// AppContext scopes the session to what the hosting app is working on
		// (see ChatSession.AppContext). Apps set it server-side on the way past,
		// where the open document is already known, so no client has to carry it.
		AppContext string `json:"app_context,omitempty"`
		// Hidden marks the user message as already-shown-in-prior-bubble
		// (e.g. an ask_user card's submitted state). LLM still sees the
		// content in history; the chat panel just skips rendering a
		// duplicate user bubble.
		Hidden    bool     `json:"hidden,omitempty"`
		Images    []string `json:"images,omitempty"` // base64-encoded image data from the chat panel's paperclip
		Documents []struct {
			Name     string `json:"name"`
			MimeType string `json:"mime_type"`
			Data     string `json:"data"` // base64
		} `json:"documents,omitempty"` // pdf / docx / text attachments — server extracts and prepends to Message
		// IntakeValues, when set, marks this submission as the agent's
		// intake form. Persisted on the user message so re-edit on
		// replayed sessions can rehydrate the form with original values.
		IntakeValues map[string]string `json:"intake_values,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Extract attached documents (PDFs, DOCX, text files). Hold them
	// in extractedAttachments for now — the preamble build runs LATER,
	// after the chat session is resolved, so we have a workspace dir
	// available for spill-on-large.
	//
	// Why split phases: a 200-page PDF text-extracts to ~500KB–2MB of
	// plain text. Prepending that whole thing to req.Message blows the
	// model's context in one round, even before any tool calls. Large
	// attachments instead spill to <workspace>/.attachments/<name>.txt
	// and inject a stub preamble (filename + size + sample + path +
	// directive to use workspace stat/head/grep/read_lines) so the LLM
	// pulls only the slices it needs.
	type extractedAttachment struct {
		name     string
		mime     string
		text     string
		failNote string // non-empty when decode / extraction failed
	}
	const minIngestChars = 200
	var extractedAttachments []extractedAttachment
	type ingestPair struct{ name, text string }
	var ingestQueue []ingestPair
	if len(req.Documents) > 0 {
		for _, d := range req.Documents {
			raw, err := base64.StdEncoding.DecodeString(d.Data)
			if err != nil {
				extractedAttachments = append(extractedAttachments, extractedAttachment{
					name:     d.Name,
					failNote: fmt.Sprintf("[attached %s — decode failed: %v]\n\n", d.Name, err),
				})
				continue
			}
			text, err := ExtractDocument(r.Context(), DocumentAttachment{
				Name:     d.Name,
				MimeType: d.MimeType,
				Data:     raw,
			})
			if err != nil {
				extractedAttachments = append(extractedAttachments, extractedAttachment{
					name:     d.Name,
					failNote: fmt.Sprintf("[attached %s — extraction failed: %v]\n\n", d.Name, err),
				})
				continue
			}
			extractedAttachments = append(extractedAttachments, extractedAttachment{
				name: d.Name,
				mime: d.MimeType,
				text: text,
			})
			if agent.IngestAttachments && len(strings.TrimSpace(text)) >= minIngestChars {
				ingestQueue = append(ingestQueue, ingestPair{name: d.Name, text: text})
			}
		}
		// Provisionally satisfy the "message or attachment required"
		// check below: a non-empty marker stands in for the eventual
		// preamble we'll write after the session exists. Replaced
		// verbatim with the real preamble after session create.
		if strings.TrimSpace(req.Message) == "" {
			for _, a := range extractedAttachments {
				if a.failNote != "" || strings.TrimSpace(a.text) != "" {
					req.Message = "[attachments-being-processed]"
					break
				}
			}
		}
		// Background ingest — runs outside the reply path so the user
		// doesn't wait on embedding latency. Each attachment gets its
		// own chunk (filename as subject) under the agent's knowledge
		// store, topic="attachments". Later turns retrieve them via
		// knowledge_search like any other ingested content.
		if len(ingestQueue) > 0 {
			go func(items []ingestPair, user, agentID string) {
				// Recover — this runs off the reply path, so a panic in
				// the embedding backend or vector-store write would
				// otherwise crash the whole server. Match the title
				// goroutine's guard above.
				defer func() {
					if r := recover(); r != nil {
						Log("[orchestrate.attachments] ingest panicked for agent=%s: %v", agentID, r)
					}
				}()
				for _, it := range items {
					ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
					ingestAgentKnowledge(ctx, T.DB, user, agentID, "attachments", it.name, it.text)
					cancel()
					Log("[orchestrate.attachments] ingested %q (%d chars) into agent=%s knowledge[attachments]", it.name, len(it.text), agentID)
				}
			}(ingestQueue, user, agent.ID)
		}
	}
	// Validate AFTER extraction so attachment-only sends (e.g. "here's
	// a resume, analyze it" with only a PDF and no typed text) are
	// accepted as long as SOMETHING came in. The message can be empty
	// only when at least one image is attached (vision tier picks it
	// up via Message.Images) — pure documents already wrote their
	// preamble into req.Message above, so an empty message past this
	// point means nothing was attached at all.
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 {
		http.Error(w, "message or attachment required", http.StatusBadRequest)
		return
	}
	// Attachment-only turns get a synthetic message so the LLM has
	// something to react to. For image-only turns the vision LLM
	// sees the images directly; we just need a non-empty content
	// field for the message-role/Content contract.
	if strings.TrimSpace(req.Message) == "" {
		if len(req.Images) > 0 {
			req.Message = "[attached image — please analyze]"
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Channel agents no longer force every send onto one thread — the client
	// sends the channel session id (cortexSessionID) when the Channel row is
	// open, and ordinary session ids otherwise. The channel thread is created
	// under its requested id on first turn just like any session.
	// Resolve session (create on first turn, otherwise load).
	sess, _ := loadChatSession(udb, agent.ID, req.SessionID)
	isNewSession := sess.ID == ""
	if isNewSession {
		sess = ChatSession{
			// Honor the requested id so a pinned session (the Operator's
			// "operator-thread") is actually CREATED under that id on its
			// first turn. Empty req.SessionID (a normal new chat) falls
			// through to saveChatSession minting a fresh UUID.
			ID:        req.SessionID,
			AgentID:   agent.ID,
			Title:     titleFromFirstMessage(req.Message),
			Created:   time.Now(),
			Incognito: req.Incognito, // clean-room session, set at creation
			// What this conversation is about, for an app that scopes its
			// sessions to a document.
			AppContext: req.AppContext,
		}
		var err error
		sess, err = saveChatSession(udb, sess)
		if err != nil {
			// Bare sseWriter for this one error; the run hasn't been
			// created yet so there's no buffer to tee into.
			tmp := newSSEWriter(w)
			tmp.Send(map[string]any{"kind": "error", "text": "save session: " + err.Error()})
			return
		}
	}

	// Back-fill the scope onto a session that has none — one that predates
	// AppContext, or was started somewhere that did not set it. Continuing a
	// conversation beside a document is a clear enough statement of what it is
	// about. Never OVERWRITES: a session belongs to where it started.
	if !isNewSession && sess.AppContext == "" && req.AppContext != "" {
		sess.AppContext = req.AppContext
	}

	// The user is actively in this session for the whole turn, so clear its
	// unread once the turn (and its final reply-save) is done. Registered
	// early so it runs LAST (LIFO), after every other save; reads the freshest
	// persisted LastAt. Background wakes use RunAgentSyncContinuing instead —
	// they never mark seen, which is what leaves a wake unread.
	defer func() { markSessionSeen(udb, agent.ID, sess.ID) }()

	// Build the attachment preamble now that the chat session (and a
	// resolvable workspace path) is available. Large extractions spill
	// to <workspace>/.attachments/<sanitized-name>.txt and inject a
	// stub; small ones inline as before via FormatAttachmentPreamble.
	//
	// Workspace dir: per-user root (same default the agent loop falls
	// back to in newToolSession). The agent loop's later ToolSession
	// resolves to the same directory unless a workspace(create/use)
	// switched to a managed one mid-turn — which doesn't happen before
	// the first user message anyway. So a spilled attachment is always
	// readable via workspace(action="head"/"grep"/...) at the path the
	// stub prints.
	if len(extractedAttachments) > 0 {
		attachSess := &ToolSession{
			Username:      user,
			ChatSessionID: sess.ID,
		}
		if ws, err := EnsureWorkspaceDir(user); err == nil {
			attachSess.WorkspaceDir = ws
		}
		var preamble strings.Builder
		for _, a := range extractedAttachments {
			if a.failNote != "" {
				preamble.WriteString(a.failNote)
				continue
			}
			preamble.WriteString(buildAttachmentPreamble(attachSess, a.name, a.mime, a.text))
		}
		// Replace the provisional marker if it was used, else prepend.
		if req.Message == "[attachments-being-processed]" {
			req.Message = strings.TrimRight(preamble.String(), "\n")
		} else if preamble.Len() > 0 {
			req.Message = preamble.String() + req.Message
		}
	}

	// Detach the agent loop's lifetime from the HTTP request. Earlier
	// the loop derived its ctx from r.Context(), so a client disconnect
	// (browser nav-away, network blip, desktop overlay opening) killed
	// every in-flight tool call mid-turn. Now ctx is rooted at
	// Background; cancellation is explicit — either /api/cancel from
	// the user, or the run-registry's auto-replace when a fresh turn
	// starts on the same session.
	//
	// The run tees every SSE frame into an in-memory buffer (see runs.go),
	// so a fresh /api/runs/<id>/stream subscriber after a reconnect
	// can replay the conversation from any sequence number.
	//
	// Detaching the LIFETIME must not detach the OWNERSHIP: the turn's
	// tokens are still this request's spend. Carry the request-scoped
	// usage tracker across (CarryRequestUsage) or the middleware's
	// end-of-request cost report sees a zero delta and prints nothing —
	// which is what silently took the per-turn "Worker/Lead tokens,
	// Searches, Est. cost" block out of the log in v0.6.159.
	ctx, cancel := context.WithCancel(CarryRequestUsage(context.Background(), r.Context()))
	run := T.runsRegistry().Create(user, agent.ID, sess.ID, cancel).
		Describe("chat", agent.Name, truncateObs(req.Message, 100))
	// Tag the ctx with this run's ID so any sub-agent dispatched during the turn
	// records it as parent — the chain the live surface renders as a tree. This
	// ctx flows into turn.ctx below, which the dispatch tools inherit.
	ctx = withParentRun(ctx, run.ID)
	sse := newTeeSSEWriter(w, run)

	// SSE response headers — handleSend streams frames inline off this response.
	// text/event-stream + X-Accel-Buffering:no so neither a reverse proxy nor the
	// browser holds frames back (matches handleRunsStream, which set these; this
	// handler historically relied on content sniffing + flush alone). Set before
	// the first frame is written.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Disconnect watchdog: when the original request's context fires
	// (client navigated away, network blip, the desktop app quit),
	// drop the live HTTP-response writer from sse. The loop runs to
	// completion thanks to the detached ctx above; this prevents
	// sse.Send from wedging the entire turn on a write to a dead
	// TCP connection (the symptom: the loop hangs after a tool exit
	// because the post-tool activity-event Send blocks holding the
	// sseWriter mutex). Run-buffer subscribers (a fresh
	// /api/runs/<id>/stream after the client reconnects) keep
	// receiving events fine.
	go func() {
		<-r.Context().Done()
		sse.detachLive()
	}()

	// inflightCancels stays populated for /api/cancel backward compat
	// (the cancel endpoint key is sessionID, same shape as before).
	inflightCancels.Store(sess.ID, cancel)
	defer func() {
		cancel()
		inflightCancels.Delete(sess.ID)
		// Mark the run done. Complete is idempotent; the panic
		// recovery defer below runs FIRST (LIFO) and gets to upgrade
		// to Failed if a panic occurred, so the unconditional
		// Completed here is the "no panic happened" path.
		run.Complete(RunStatusCompleted)
	}()

	// Always carry the run ID alongside session so the chat panel can
	// open /api/runs/<id>/stream after a reconnect. session event
	// keeps its prior shape for backward compat.
	sse.Send(map[string]any{"kind": "session", "id": sess.ID})
	sse.Send(map[string]any{"kind": "run", "id": run.ID})
	// Surface any panic in the turn pipeline through the SSE stream
	// AND the gohort log. The http server has its own panic recovery
	// that just tears down the connection; without this, a silent
	// panic in runPlan / runWorkerStep / synthesis looks like "no
	// reply" to the user and shows no clue in the log.
	//
	// Runs first in LIFO order so it gets to mark the run Failed
	// before the outer defer marks it Completed (Run.Complete is
	// idempotent — first call wins).
	defer func() {
		if rec := recover(); rec != nil {
			Log("[orchestrate /api/send] PANIC recovered: %v", rec)
			sse.Send(map[string]any{"kind": "error", "text": fmt.Sprintf("server panic: %v", rec)})
			run.Complete(RunStatusFailed)
		}
	}()

	// Append the user message to the persisted thread now; failures
	// downstream still leave the user's input visible on reload.
	userMsg := ChatMessage{Role: "user", Content: req.Message, Created: time.Now(), Hidden: req.Hidden}
	if len(req.IntakeValues) > 0 {
		userMsg.IntakeValues = req.IntakeValues
	}
	sess.Messages = append(sess.Messages, userMsg)
	if saved, err := saveChatSession(udb, sess); err == nil {
		sess = saved
	}
	// Ongoing-conversation compaction (channel home thread only): the RUN sees a
	// bounded view — a running summary + the ContextDepth tail — and STORAGE is
	// likewise bounded to the summary + a generous tail (trimStoredHistory), with
	// folded-out spans archived to the recall index rather than kept inline, so
	// this long-lived thread doesn't grow without limit. Folds aging turns into
	// the summary and evicts durable facts into memory. Gated to the channel
	// thread so a channel agent's ordinary ad-hoc sessions stay verbatim (they're
	// disposable; only the persistent home thread needs the running summary).
	//
	// CRITICAL: the bounded view is RUN-ONLY — it feeds runPlan below and
	// nothing else. It must NOT be written back to sess.Messages: the
	// CompactState.SummarizedThrough cursor indexes the FULL history, so
	// re-feeding an already-shortened list overshoots the cursor, clamps the
	// tail to empty, and silently drops the user's just-typed message from
	// BOTH the LLM payload and (via the end-of-turn save) storage — the model
	// then answers from the stale summary alone. Keep sess.Messages full; the
	// dispatch path (agent_dispatch.go) already uses this run-only pattern.
	planMsgs := sess.Messages
	// Bound any PERSISTENT thread — the Cortex home thread AND every channel
	// room. Both are keyed "channel:<id>" and both accumulate turn after turn
	// with no fresh-session reset, so both grow until they blow the context
	// window (observed: a channel room reached 299k tokens against a 262k
	// window, force-compact couldn't recover, and the turn died at round 0).
	// The gate used to require agent.Cortex, so a channel agent with the Cortex
	// FEATURE off ran this whole path unbounded — and context_depth read as
	// "default (12)" while never taking effect, because the code that applies
	// that default is exactly what the gate skipped. A normal chat session is
	// safe unbounded: it gets a fresh id per conversation and is short-lived.
	//
	// ALSO bound an ordinary session that has outgrown the assumption above.
	// "A normal chat session is short-lived" is a habit, not an invariant:
	// nothing makes anyone start a new one, and a dashboard thread somebody
	// keeps using for weeks grows without limit while this gate excludes it
	// from the only thing that would stop it. Observed live at ~1.4M tokens of
	// conversation against a 262k window, failing every turn and unrecoverable
	// downstream — the loop's own salvage can fold history, but a thread that
	// arrives that large has already cost the turn. Small sessions still skip
	// this entirely and stay verbatim, which is what the gate was protecting.
	if strings.HasPrefix(sess.ID, "channel:") || historyOutgrewItsSession(sess.Messages, T.LeadContextSize()) {
		planMsgs = T.compactOperatorHistory(udb, user, agent, sess.ID, sess.Messages)
		// Bound STORAGE too (not just the run-view): drop leading messages already
		// folded into the summary AND archived to the recall index, keeping the
		// summary + a generous verbatim tail. Caps the per-turn (and 3s cortex-poll)
		// load of this long-lived thread instead of growing it forever; older
		// content stays recoverable via recall_history. Cursor stays consistent.
		sess.Messages = T.trimStoredHistory(udb, agent, sess.ID, sess.Messages)
	}

	if T.LLM == nil {
		sse.Send(map[string]any{"kind": "error", "text": "worker LLM not configured"})
		return
	}

	// Register the per-session interjection queue so /api/inject can
	// find one. Released at the end of the turn so a subsequent
	// inject hits "session not interjectable" rather than landing
	// notes nobody will drain.
	//
	// Before release, drain any notes still queued — they arrived
	// after the last in-turn drain point (during synthesis, during
	// the directReply path, etc.) and would otherwise be destroyed.
	// Drained notes are appended as user messages on the session so
	// they survive into the NEXT turn as part of normal history.
	// Defers run LIFO, so this fires before release.
	queue := registerInjectionQueue(sess.ID, user, agent.ID)
	defer releaseInjectionQueue(sess.ID)
	defer func() {
		if queue == nil {
			return
		}
		leftover := queue.Drain()
		if len(leftover) == 0 {
			return
		}
		udb := UserDB(T.DB, user)
		if udb == nil {
			return
		}
		// Reload session so we don't clobber any other writes that
		// landed during the turn (the turn struct's snapshot may be
		// stale by this defer's firing time).
		latest, ok := loadChatSession(udb, agent.ID, sess.ID)
		if !ok {
			return
		}
		now := time.Now()
		for _, n := range leftover {
			latest.Messages = append(latest.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		_, _ = saveChatSession(udb, latest)
		Log("[orchestrate.inject] preserved %d leftover note(s) into session %s for next turn", len(leftover), sess.ID)
	}()

	// ForcePrivate on the agent record locks Private mode ON
	// regardless of what the user toggled. Composes with the
	// per-turn user toggle via OR — either source can force the
	// network connector to refuse.
	privateMode := req.PrivateMode || agentForcesPrivate(agent)
	// Build ONE connector per turn and share it across ctx,
	// sess.Network, and the inflight registry. Sharing one
	// instance is what makes the mid-turn cutoff work: when the
	// privacy endpoint flips this connector via SetAllowed, every
	// in-flight tool re-checking Allowed() sees the new state.
	turnConnector := NewNetworkConnector(privateMode)
	ctx = WithNetworkConnector(ctx, turnConnector)
	inflightConnectors.Store(sess.ID, turnConnector)
	defer inflightConnectors.Delete(sess.ID)
	// Also lock ForcePrivate agents so the privacy endpoint can't
	// flip them OFF — the agent-level lock is the source of truth.
	if agentForcesPrivate(agent) {
		// no-op marker; SetAllowed(true) from the endpoint will be
		// reverted by the next Allowed() check if we add an
		// auto-revert guard. Simpler for now: trust the endpoint
		// to refuse turning private OFF when ForcePrivate is set
		// (endpoint reads the agent record).
		_ = agent.ForcePrivate
	}

	// Label every LLM call this turn makes, so the prefix watch can tell
	// consecutive calls of ONE turn apart from two turns that happen to
	// overlap. Silent unless something in the cached prefix moves between
	// calls — see core/prompt_prefix_watch.go.
	ctx = WithPromptTurn(ctx, "agent="+agent.ID+" session="+sess.ID)

	// A PUBLISHED agent can be chatted by someone who is not its author. The
	// record, and the custom-tool pool its AllowedTools names, live in the
	// AUTHOR's store; sessions, memory and knowledge stay with whoever is
	// typing. Without this the agent ran against the visitor's store, found none
	// of its own tools, and said nothing about it. Unset on an ordinary turn,
	// where the two identities are the same and ownerView falls back.
	//
	// NOT for a SEED agent. A seed's Owner is the framework marker "system",
	// which is not an author and has no store of its own: redirecting to it
	// pointed every seed agent at an empty fleet. Builder is the agent that
	// suffers most and the one that can least afford it — it is the only agent
	// with authoring access, its entire job is reading and changing the user's
	// agents, and it was being handed a store containing none of them.
	// Observed as agents(action="list") returning [] in the same turn that
	// survey listed thirty-eight, every lookup failing, and Builder telling
	// the user their agent must have been deleted.
	var ownerUDB Database
	ownerUser := ""
	if agent.Owner != "" && agent.Owner != user && agent.Owner != seedOwner {
		ownerUser, ownerUDB = agent.Owner, UserDB(T.DB, agent.Owner)
	}
	turn := &chatTurn{
		app:         T,
		prep:        prep,
		ctx:         ctx,
		sse:         sse,
		udb:         udb,
		user:        user,
		agent:       agent,
		ownerUser:   ownerUser,
		ownerDB:     ownerUDB,
		queue:       queue,
		session:     &sess,
		privateMode: privateMode,
		network:     turnConnector,
		// Incognito (clean-room) sessions also suppress the inferred memory layer
		// (write side) — nothing accumulates back, matching "no baggage out".
		inferredDisabled: req.InferredDisabled || sess.Incognito,
		isNewSession:     isNewSession,
		userImages:       decodeUserImages(req.Images),
		// from_client_* tools are exposed only when the request came from the
		// gohort-desktop viewer (its proxy stamps the bridge key) — never a
		// remote browser/phone on the same account.
		fromDesktopClient: user != "" && DesktopClientUser(r) == user,
		// Flag the turn as having fresh external content if the user
		// attached any document. The consolidation loop-break gate
		// reads this to decide whether to ingest the synthesis.
		userDocsThisTurn: len(req.Documents) > 0,
		deliveredSkills:  map[string]bool{},
		appTools:         appTools,
	}
	// Bound before anything can run, so a card emitted by the very first
	// tool call still has somewhere to go.
	appBlockPub.bind(turn)
	for _, d := range req.Documents {
		if n := strings.TrimSpace(d.Name); n != "" {
			turn.docNames = append(turn.docNames, n)
		}
	}
	// (Skills are per-turn now: turn.skillsActive starts empty and is
	// populated only by activate_skill calls THIS turn. Nothing is
	// rehydrated from the session — activation is not sticky across turns.)
	// Reset the session's "awaiting user confirm" flag — the two-turn
	// gate that read this is removed. Kept here to clear stale state
	// from older sessions that may have set it; if THIS turn ends with
	// ask_user again the dispatch path re-sets it as needed.
	sess.AwaitingUserConfirm = false
	// Lift the authoring-in-progress slot from its side-table onto the
	// in-memory ChatSession so chatTurn-bound tool handlers (notably
	// create_pipeline_tool) can read it without their own DB lookup.
	// Side-table because the global create_agent handler can write
	// without knowing the per-agent session-storage shape.
	if a := loadAuthoringInProgress(udb, sess.ID); a != "" {
		sess.AuthoringAgentID = a
	}

	// Note: orchestrate does NOT auto-promote follow-ups into a prior
	// sub-agent. Dispatch (agents(run)) is synchronous and one-shot —
	// its reply is a tool result the orchestrator synthesizes from.
	// Continued conversation with a sub-agent happens the normal way:
	// the user keeps chatting (unlimited turns, full session history)
	// and the LLM re-calls agents(run) when it wants to delegate again
	// — which re-threads the same sub-session by its deterministic ID.
	// Auto-promotion was removed: it routed worker-mode dispatches as
	// conversational replies (producing empty bubbles) and was
	// redundant with just chatting. Injection/promotion remain a
	// phantom concern, where async dispatch makes them meaningful.

	// (Per-turn topic auto-classifier removed. The LLM picks the
	// topic slug when it calls memory_save / memory_search via the
	// topic= arg — the system prompt's "Known topics" block shows
	// existing slugs to nudge reuse. Without the classifier, this
	// turn starts unscoped; tools fall back to generalTopic when
	// the LLM doesn't pass one.)

	// Fire a fast acknowledgment concurrently so the user sees a
	// contextual "On it…" while round-1 planning (thinking mode)
	// produces its first real output. Skipped automatically for
	// greetings / trivial asks (the ack call returns NONE). Promotion
	// turns already emit their own "Routing follow-up…" status and
	// returned above, so this only runs on main-LLM turns.
	//
	// DISABLED by default: the ack is a 3rd concurrent LLM call that
	// competes with this turn's own lead + worker calls for the same
	// backend slots. On constrained servers (llama.cpp --parallel <= 2)
	// it never lands — it just queues, times out at ackTimeout, and adds
	// latency + log noise — while its value is purely cosmetic and the
	// "Thinking…" status below already fills the dead air. Flip
	// ackEnabled to true on a server with spare slots to re-enable.
	if ackEnabled {
		go turn.emitAck(ctx, req.Message)
	}

	// --- Orchestrator round 1: respond directly, plan, or ask ---
	turn.emitStatus("Thinking…")
	steps, question, directReply, planErr := turn.runPlan(planMsgs)
	if planErr != nil {
		persistIncompleteTurnTrace(&sess, udb, turn, planErr.Error())
		sse.Send(map[string]any{"kind": "error", "text": "plan: " + planErr.Error()})
		return
	}
	if directReply != "" {
		// runPlan already emitted the bubble live — either streamed inline during
		// the agent loop, or promoted post-loop via emitCapturedAsBubble (which
		// suppresses only a true near-duplicate of the last streamed bubble). Just
		// persist + run background consolidation + title generation.
		turn.emitStatus("Direct response.")
		orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), directReply)
		finalCalls := append(orphanCalls, turn.persistedToolCalls()...)
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: directReply,
			Created: time.Now(), Usage: turn.drainLastUsage(),
			// ToolCalls: the orchestrator's tool log this turn — same
			// data the plan_set path persists at the bottom of this
			// function. orphanCalls picks up any tool records from
			// mid-turn bubbles that got dedup'd as near-duplicates of
			// directReply — without that merge, short turns lose the
			// only carrier of their tool record.
			ToolCalls: finalCalls,
		})
		_, _ = saveChatSession(udb, sess)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	if question != "" {
		// runPlan already emitted the question bubble. Just persist
		// + end the turn — the user's next message will re-enter the
		// plan round with the answer in context.
		turn.emitStatus("Orchestrator asked for clarification.")
		orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), question)
		finalCalls := append(orphanCalls, turn.persistedToolCalls()...)
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: question,
			Created: time.Now(), Usage: turn.drainLastUsage(),
			// ToolCalls: ask_user / ask_user_form are themselves tool
			// calls; record everything that fired this turn (including
			// the ask itself) so the export shows what led to the
			// question. orphanCalls preserves tool records from
			// dedup'd mid-turn bubbles — symmetric with directReply.
			ToolCalls: finalCalls,
		})
		_, _ = saveChatSession(udb, sess)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	// syntheticPlan marks the degenerate case: there IS no plan, and the step
	// below exists only so the pipeline still produces a worker output. It must
	// not RENDER as a plan — a turn that decided "no plan needed" and then
	// displays "Plan: 1. Respond directly" is pure ceremony, and it is the
	// first thing a user sees on a turn that was meant to be a direct answer.
	syntheticPlan := false
	if len(steps) == 0 {
		// Degenerate: orchestrator chose neither to plan nor to ask.
		// Run a single pseudo-step "Respond" so the flow still
		// produces a worker output and a synthesis reply.
		syntheticPlan = true
		turn.emitStatus("No plan needed — direct response.")
		steps = []PlanStep{{ID: 1, Title: "Respond directly", Status: StepPending}}
	} else {
		turn.emitStatus(fmt.Sprintf("Plan committed — %d step%s.", len(steps), plural(len(steps))))
	}

	// Plan and per-step status all render as a single in-chat block;
	// the runner re-emits the same block id on each transition and the
	// dispatcher routes the repeat into the renderer's onUpdate.
	roundIdx := len(sess.Plans)
	blockID := planBlockID(sess.ID, roundIdx)
	if syntheticPlan {
		// Empty id = every emitPlanBlock call this round is a no-op, including
		// the per-step transition re-emits further down.
		blockID = ""
	}
	emitPlanBlock(sse, blockID, steps)

	// --- Per-step worker execution ---
	for i := range steps {
		select {
		case <-ctx.Done():
			sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
			return
		default:
		}
		// A control tool closed the turn. Steps are driven HERE, outside the
		// orchestrator's RunAgentLoop, so a tool that ends that loop never
		// reached this queue: the model called stay_silent, read "this turn is
		// now closed", and then watched the plan keep executing with no way to
		// intervene. Closing a turn abandons the pending plan — which is what
		// the tool already tells the model it does.
		if turn.turnClosed {
			Log("[orchestrate.orch] turn closed by a control tool — abandoning %d remaining step(s)", len(steps)-i)
			turn.emitStatus(fmt.Sprintf("Turn closed — %d remaining step%s skipped.", len(steps)-i, plural(len(steps)-i)))
			for j := i; j < len(steps); j++ {
				steps[j].Status = StepBlocked
				steps[j].BlockedReason = "turn closed before this step ran"
			}
			emitPlanBlock(sse, blockID, steps)
			break
		}
		steps[i].Status = StepInProgress
		emitPlanBlock(sse, blockID, steps)
		// Servitor-style intent narration block lands in the
		// conversation pane before the worker fires so the user
		// sees "▸ Investigating: Step N: <title> — <intent>"
		// inline. Activity-pane status row stays too for the
		// right-pane audit trail.
		emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
		turn.emitStatus(fmt.Sprintf("▸ Step %d/%d: %s", steps[i].ID, len(steps), steps[i].Title))

		// Pull anything the user has queued since the prior step (or
		// since the plan was set). drainNotes persists them as user
		// ChatMessages and emits notes_consumed so client bubbles
		// re-style as "agent saw your note".
		stepNotes := turn.drainNotes()
		if n := len(stepNotes); n > 0 {
			turn.emitStatus(fmt.Sprintf("Picked up %d user note%s mid-flight.", n, plural(n)))
		}
		out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
		if err != nil {
			steps[i].Status = StepBlocked
			steps[i].BlockedReason = err.Error()
			steps[i].Output = "error: " + err.Error()
			emitPlanBlock(sse, blockID, steps)
			turn.emitStatus(fmt.Sprintf("✗ Step %d/%d failed: %s", steps[i].ID, len(steps), truncate(err.Error(), 120)))
			continue
		}
		steps[i].Status = StepDone
		steps[i].Output = out
		steps[i].Findings = deriveFindings(out)
		emitPlanBlock(sse, blockID, steps)
		// Completion status — show what was actually accomplished
		// instead of leaving the user with the pre-step "▸ Step N:
		// <title>" line. Uses the same derived findings the plan
		// card shows; if findings are empty (worker returned
		// nothing meaningful), fall back to the title.
		summary := steps[i].Findings
		if summary == "" {
			summary = steps[i].Title
		}
		turn.emitStatus(fmt.Sprintf("✓ Step %d/%d: %s", steps[i].ID, len(steps), truncate(summary, 160)))
	}

	// --- Post-plan gap detection (opt-in) ---
	// Research-flavored agents set GapCheck=true so the runner scans
	// the worker outputs for structural failure modes (abstract
	// claims, evidence asymmetry, mechanism gaps) and runs targeted
	// fills before synthesis. Chat-flavored agents leave it off —
	// gap detection is a cost the conversational surface doesn't
	// need to pay every turn.
	if agent.GapCheck && len(steps) > 0 {
		turn.emitStatus("Checking for gaps…")
		gaps := turn.runGapCheck(req.Message, steps, len(steps)+1)
		if len(gaps) > 0 {
			turn.emitStatus(fmt.Sprintf("Found %d gap%s — filling…", len(gaps), plural(len(gaps))))
			// Append the new steps onto the plan and re-emit the
			// block so the user sees them join the existing card.
			steps = append(steps, gaps...)
			emitPlanBlock(sse, blockID, steps)
			for i := len(steps) - len(gaps); i < len(steps); i++ {
				select {
				case <-ctx.Done():
					sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
					return
				default:
				}
				steps[i].Status = StepInProgress
				emitPlanBlock(sse, blockID, steps)
				emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
				turn.emitStatus(fmt.Sprintf("▸ Gap %d/%d: %s", i-(len(steps)-len(gaps))+1, len(gaps), steps[i].Title))
				stepNotes := turn.drainNotes()
				out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
				gapPos := i - (len(steps) - len(gaps)) + 1
				if err != nil {
					steps[i].Status = StepBlocked
					steps[i].BlockedReason = err.Error()
					steps[i].Output = "error: " + err.Error()
					emitPlanBlock(sse, blockID, steps)
					turn.emitStatus(fmt.Sprintf("✗ Gap %d/%d failed: %s", gapPos, len(gaps), truncate(err.Error(), 120)))
					continue
				}
				steps[i].Status = StepDone
				steps[i].Output = out
				steps[i].Findings = deriveFindings(out)
				emitPlanBlock(sse, blockID, steps)
				summary := steps[i].Findings
				if summary == "" {
					summary = steps[i].Title
				}
				turn.emitStatus(fmt.Sprintf("✓ Gap %d/%d: %s", gapPos, len(gaps), truncate(summary, 160)))
			}
		} else {
			turn.emitStatus("No gaps detected.")
		}
	}

	// --- Orchestrator synthesis round ---
	synthNotes := turn.drainNotes()
	if n := len(synthNotes); n > 0 {
		turn.emitStatus(fmt.Sprintf("Picked up %d user note%s before synthesis.", n, plural(n)))
	}
	turn.emitStatus("Composing reply…")
	reply, synthErr := turn.runSynthesis(req.Message, steps, synthNotes)
	if synthErr != nil {
		persistIncompleteTurnTrace(&sess, udb, turn, synthErr.Error())
		sse.Send(map[string]any{"kind": "error", "text": "synthesis: " + synthErr.Error()})
		return
	}

	// Persist the orchestrator's final reply + the plan snapshot +
	// the full tool-call trace for this turn (orchestrator rounds +
	// worker steps). The trace makes session export useful for
	// debugging and dataset capture without needing live SSE.
	// orphanCalls picks up tool records carried by mid-turn bubbles
	// that got dedup'd as near-duplicates of the synthesis reply.
	turnToolCalls := turn.persistedToolCalls()
	orphanCalls := appendMidTurnBubbles(&sess, turn.drainMidTurnBubbles(), reply)
	finalCalls := append(orphanCalls, turnToolCalls...)
	// The model did the work (tools fired) but wrote no summary — a blank bubble
	// renders as nothing, so the turn looks like it vanished. Mark it so the tool
	// trace stays reachable in history.
	if strings.TrimSpace(reply) == "" && len(finalCalls) > 0 {
		reply = "_(No written reply this turn — see the tool actions above.)_"
	}
	// Delivery backstop: the reply SAYS it sent a picture and nothing was
	// attached. The channel path has had this for a while; chat never did, so
	// "here's your image" with no image was a dead end here — the file sat in
	// the workspace, correctly produced, and the model simply never made the
	// second call that ships it.
	//
	// Gated on the model's own delivery CLAIM, which is what keeps it honest:
	// it only ever ships a file the model said it was sending, not whatever
	// happens to be lying in the workspace.
	turn.recoverClaimedDelivery(reply)
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "assistant", Content: reply,
		Created: time.Now(), Usage: turn.drainLastUsage(),
		ToolCalls: finalCalls,
		// The live stream already painted these; carrying their ids is what
		// makes them survive a reload. Until now an image the agent delivered
		// existed only as an SSE event, so reopening the thread showed the text
		// that described a picture and no picture.
		Attachments: turn.takeDeliveredAttachments(),
	})
	sess.Plans = append(sess.Plans, PlanSnapshot{
		RoundIndex: len(sess.Plans),
		Steps:      steps,
		Synthetic:  syntheticPlan,
	})
	_, _ = saveChatSession(udb, sess)

	// Hallucinated-authoring detection. The pattern we keep seeing
	// on smaller models: the assistant replies "I've created the
	// agent with tools X, Y, Z…" without ever firing add_tool /
	// create_agent this turn. Catch it by cross-checking three
	// signals — (1) build plan exists with pending steps, (2) zero
	// authoring tools fired this turn, (3) the reply has non-empty
	// text. If all three: surface a visible warning AND inject a
	// corrective note into the session so the next turn's history
	// shows the LLM exactly what it claimed vs what actually fired.
	mismatchFired := injectAuthoringMismatchWarning(&sess, turnToolCalls, reply)
	if mismatchFired {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "Build plan still has pending steps but no authoring tool fired this turn — the reply may be describing work that didn't actually happen. Next turn will be re-prompted.",
		})
	}
	// The third quadrant: no build plan at all, nothing errored because nothing
	// fired, and the reply is a forward-looking PROMISE to author rather than a
	// claim it's done. Suppressed when the mismatch check already fired so a
	// planless promise gets exactly one notice.
	if !mismatchFired && injectPromisedAuthoringWarning(&sess, turnToolCalls, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "The reply promised to create or update a tool or agent but no authoring call fired this turn — nothing was saved. Next turn is re-prompted to actually make the call.",
		})
	}
	// A fourth shape: the reply claims a tool is UNAVAILABLE when that tool
	// is sitting in the agent's own catalog. Builder did this with tool_def
	// — narrated "I cannot access tool_def, this is a framework issue,
	// report it to the administrators" across three turns without ever
	// emitting the call. A real refusal produces an ERROR tool result the
	// user can see; a hallucinated one produces confident prose blaming the
	// platform, which is worse than a plain failure because it sends the
	// user off to debug something that isn't broken.
	if injectFalseUnavailabilityWarning(&sess, turnToolCalls, reply, agent) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "The reply claimed a tool is unavailable, but that tool is in this agent's catalog and was never called. Next turn is re-prompted to actually call it.",
		})
	}
	// The other half of hallucinated authoring: an authoring tool DID fire but
	// every call errored, yet the reply claims success (the live moltbook case —
	// no BuildPlan, so the check above misses it).
	if injectFailedAuthoringWarning(&sess, turnToolCalls, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "Every authoring call this turn failed, but the reply claims it's done — the tool/agent was NOT saved. Next turn is re-prompted to actually fix it.",
		})
	}
	// The build closed out without ever running the gap check, so nothing
	// surfaced blocked steps or unverified tools. Same post-hoc shape as the two
	// checks above rather than a mid-turn block: the reply has already streamed
	// to the client, and discarding it to force another round re-renders the
	// content (see the correction/re-render rule).
	if injectSkippedGapReportWarning(&sess, udb, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "The build plan was closed out without calling report_build_gaps — blocked steps and unverified tools went unchecked. Next turn is re-prompted to verify before claiming done.",
		})
	}

	turn.titleAfterFirstTurn()

	sse.Send(map[string]any{"kind": "done"})
}

// handleCancel aborts an in-flight runner by session ID. The runner
// goroutine cleans up on ctx.Done.
func (T *OrchestrateApp) handleCancel(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	// The Agency chat panel POSTs the session id as the ?id= query param (no
	// body); older callers send {session_id} in the JSON body. Accept BOTH —
	// reading only the body meant the Agency cancel button silently no-opped
	// (empty body → empty id → cancel() never fired, loop ran to completion).
	sid := strings.TrimSpace(r.URL.Query().Get("id"))
	if sid == "" {
		var body struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sid = strings.TrimSpace(body.SessionID)
	}
	if sid != "" {
		if v, ok := inflightCancels.Load(sid); ok {
			if cancel, ok := v.(context.CancelFunc); ok {
				cancel()
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
