// Package openaiapi serves an OpenAI-compatible /v1/chat/completions endpoint
// so an external platform that only knows how to talk to "an OpenAI API" can
// drive gohort — either a raw model or a full agent.
//
// The motivating case is a voice platform's custom-LLM setting (Vapi and
// friends): you give it a base URL and a key, it POSTs the OpenAI chat shape
// with stream:true and expects SSE deltas back. Nothing else about those
// platforms is negotiable, so the adapter lives here rather than asking the
// caller to speak gohort's own protocol.
//
// The `model` field is the router, which is what lets one endpoint serve both
// readings of "use my LLM":
//
//	"worker" / "lead"   → straight through to that tier. Real token streaming,
//	                      no tools, no memory, no persona. This is the low-
//	                      latency option and the right one for live voice.
//	"agent:<id-or-name>" → a full agent turn — its persona, tools, and memory —
//	                      in a thread private to this caller.
//	"channel:<chat>"     → the same, but INSIDE an existing conversation: the
//	                      agent bound to that chat, running on that chat's own
//	                      thread, so it arrives holding the room's history and
//	                      what it says on the call stays part of that thread.
//
// All three routes stream tokens as they generate. The agent routes still cost
// more than the tiers — a turn that reaches for tools spends a full round trip
// per tool — so keep a voice-facing agent's tool set small and budget for the
// caller's response timeout.
//
// Which agents are reachable is governed by the SAME "Reachable over MCP"
// toggle that gates the MCP server, so external exposure stays one switch per
// agent instead of two that can disagree.
//
// Not enabled by default — add a blank import to agents.go to mount it:
//
//	_ "github.com/cmcoffee/gohort/apps/openaiapi"
package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// OpenAIFeatureKey is the shareable-feature id gating the /v1 endpoint. The
// admin controls which users may use it (FeatureAllowedForUser); a user's key
// then opts in per key via TokenScope.Features. Exported so the enforcement
// path and any test reference one constant.
const OpenAIFeatureKey = "openai"

func init() {
	RegisterApp(new(OpenAIAPI))
	// Declare the /v1 endpoint as an admin-gateable feature. Absent an admin
	// policy it's open to all users (non-breaking on the live integration); the
	// admin narrows it under Admin → Feature Access.
	RegisterShareableFeature(ShareableFeature{
		Key:   OpenAIFeatureKey,
		Label: "OpenAI-compatible /v1 endpoint",
		Desc:  "Let a user expose their agents to external clients (voice platforms, OpenAI SDKs) through their own personal access tokens.",
	})
}

// OpenAIAPI is the app entry point.
type OpenAIAPI struct {
	AppCore
}

func (T OpenAIAPI) Name() string         { return "openai_api" }
func (T OpenAIAPI) SystemPrompt() string { return "" }
func (T OpenAIAPI) Desc() string {
	return "Apps: OpenAI-compatible /v1 endpoint for external clients (voice platforms, SDKs)."
}

func (T *OpenAIAPI) Init() error { return T.Flags.Parse() }

func (T *OpenAIAPI) Main() error {
	Log("openai_api is an endpoint-only app. Start with: gohort serve")
	return nil
}

func (T *OpenAIAPI) WebPath() string { return "/v1" }
func (T *OpenAIAPI) WebName() string { return "OpenAI-compatible API" }
func (T *OpenAIAPI) WebDesc() string {
	return "Lets an external client drive a model or an agent over the OpenAI chat API."
}

// WebHidden keeps the endpoint mounted without a dashboard tile — there is no
// human-facing page here, only a machine endpoint.
func (T *OpenAIAPI) WebHidden() bool { return true }

func (T *OpenAIAPI) Routes() {
	// Public path: auth is a header (X-API-Key or Authorization: Bearer), not a
	// dashboard cookie, so it must bypass AuthMiddleware. Mirrors /mcp/.
	RegisterPublicPath("/v1/")
	T.HandleFunc("/chat/completions", T.handleChatCompletions)
	T.HandleFunc("/models", T.handleModels)
}

// --- wire types --------------------------------------------------------------

type chatReq struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"` // string, or the array form some clients send
	} `json:"messages"`
	Stream      bool    `json:"stream"`
	Temperature float64 `json:"temperature,omitempty"`
	User        string  `json:"user,omitempty"`
	// Call is Vapi's per-call envelope. Only the id is used, as a stable
	// conversation key so an agent threads the whole call instead of treating
	// every utterance as a new conversation.
	Call struct {
		ID string `json:"id"`
	} `json:"call"`
}

// textContent flattens the two content shapes clients send: a plain string, or
// an array of typed parts ([{type:"text",text:"..."}]). Non-text parts are
// skipped — this endpoint is text-in/text-out.
func textContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// isTierName reports whether a model string names a raw model tier rather than
// something that should resolve to a conversation.
func isTierName(m string) bool {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "worker", "lead", "gohort", "gohort-worker", "gohort-lead", "default":
		return true
	}
	return false
}

// canonicalTier maps a tier name (with its aliases) to the exact string the key
// scope stores — "worker" or "lead". "lead"/"gohort-lead" → "lead"; everything
// else that isTierName accepts → "worker".
func canonicalTier(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "lead", "gohort-lead":
		return "lead"
	default:
		return "worker"
	}
}

// --- access gates ------------------------------------------------------------
//
// Three gates, in order (see core/feature_access.go + core/account_token.go):
//   1. admin — may this USER use the /v1 endpoint at all (FeatureAllowedForUser)
//   2. key   — is the feature enabled on THIS key (token.AllowsFeature)
//   3. key   — is the resolved TARGET in the key's scope (token.AllowsTarget)
//
// A nil token is a non-account-token auth path (a bridge/desktop key reaching
// /v1); those have their own governance, so the per-KEY gates (2, 3) are skipped
// for them — only the per-USER admin gate applies. A legacy key (nil Scope,
// minted before scoping) grandfathers through gates 2/3 (AllowsFeature/
// AllowsTarget return true), logged once so the silent allow is visible.

// gateFeature applies gates 1 and 2. Returns false (and writes a 403) when the
// request may not use the endpoint.
func (T *OpenAIAPI) gateFeature(w http.ResponseWriter, user string, token *AccountToken) bool {
	if !FeatureAllowedForUser(T.DB, OpenAIFeatureKey, user) {
		writeErr(w, http.StatusForbidden, "the OpenAI /v1 endpoint is not enabled for your account — ask an admin to grant it under Feature Access")
		return false
	}
	if token != nil && !token.AllowsFeature(OpenAIFeatureKey) {
		writeErr(w, http.StatusForbidden, "this API key is not scoped for the OpenAI endpoint — enable it under Account → API keys → Scope")
		return false
	}
	if token != nil && token.IsLegacyUnscoped() {
		Log("[openai_api] %s: key %q predates scoping — allowed unrestricted (set a scope under Account → API keys to lock it down)", user, token.ID)
	}
	return true
}

// gateTarget applies gate 3 for a resolved canonical target. Returns false (and
// writes a 403) when the key may not reach it.
func gateTarget(w http.ResponseWriter, user string, token *AccountToken, canonical string) bool {
	if token != nil && !token.AllowsTarget(canonical) {
		writeErr(w, http.StatusForbidden, "this API key is not scoped to reach "+canonical+" — grant it under Account → API keys → Scope, or use a target the key allows (GET /v1/models lists them)")
		Log("[openai_api] %s: key %q denied target %q (not in scope)", user, token.ID, canonical)
		return false
	}
	return true
}

// --- handlers ----------------------------------------------------------------

// handleModels answers the catalog probe most OpenAI clients make before their
// first call. It lists the two tiers plus every agent the user has exposed, so
// a config UI that offers a model dropdown shows real, working choices.
func (T *OpenAIAPI) handleModels(w http.ResponseWriter, r *http.Request) {
	user := APIKeyUser(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing or invalid API key — send a personal access token from /account as X-API-Key or Authorization: Bearer")
		return
	}
	token := AccountTokenFromRequest(r)
	// Gates 1 + 2: if the endpoint isn't enabled for this user or this key, the
	// catalog is empty of choices to make — 403 rather than a misleading list.
	if !T.gateFeature(w, user, token) {
		return
	}
	// A model is listed only when the key may actually reach it, so a client's
	// dropdown shows working choices and nothing it would be 403'd for. A legacy
	// (nil-scope) key lists everything (AllowsTarget → true).
	allow := func(canonical string) bool { return token == nil || token.AllowsTarget(canonical) }
	data := []map[string]any{}
	if allow("worker") {
		data = append(data, map[string]any{"id": "worker", "object": "model", "owned_by": "gohort"})
	}
	if allow("lead") {
		data = append(data, map[string]any{"id": "lead", "object": "model", "owned_by": "gohort"})
	}
	for _, a := range orchestrate.ExternalAgents(T.DB, user) {
		if allow("agent:" + a.ID) {
			data = append(data, map[string]any{
				"id": "agent:" + a.ID, "object": "model", "owned_by": "gohort",
				"description": a.Name,
			})
		}
	}
	// Live conversations, so a config UI can offer "join THIS room" directly.
	if orch := findOrchestrate(); orch != nil {
		for _, c := range orch.ExternalChannels(user) {
			if allow("channel:" + c.ChatID) {
				data = append(data, map[string]any{
					"id": "channel:" + c.ChatID, "object": "model", "owned_by": "gohort",
					"description": c.Name + " — " + c.AgentName,
				})
			}
		}
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func (T *OpenAIAPI) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	user := APIKeyUser(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing or invalid API key — send a personal access token from /account as X-API-Key or Authorization: Bearer")
		return
	}
	token := AccountTokenFromRequest(r)
	// Gates 1 + 2 (admin permits user; key has the feature enabled).
	if !T.gateFeature(w, user, token) {
		return
	}
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages is required")
		return
	}

	target := strings.TrimSpace(req.Model)
	Log("[openai_api] %s model=%q stream=%v msgs=%d", user, target, req.Stream, len(req.Messages))

	// Gate 3 is applied against the RESOLVED CANONICAL target, so a key scoped to
	// "agent:<id>" also authorizes the bare agent name that resolves to it.
	if chatKey, isChan := strings.CutPrefix(target, "channel:"); isChan {
		chatKey = strings.TrimSpace(chatKey)
		canon := "channel:" + chatKey
		if orch := findOrchestrate(); orch != nil {
			if tgt, ok := orch.ResolveExternalChannel(user, chatKey); ok {
				canon = "channel:" + tgt.ChatID
			}
		}
		if !gateTarget(w, user, token, canon) {
			return
		}
		T.serveChannel(w, r, user, chatKey, req)
		return
	}
	if agentKey, isAgent := strings.CutPrefix(target, "agent:"); isAgent {
		agentKey = strings.TrimSpace(agentKey)
		canon := "agent:" + agentKey
		if id, ok := orchestrate.ResolveExternalAgent(T.DB, user, agentKey); ok {
			canon = "agent:" + id
		}
		if !gateTarget(w, user, token, canon) {
			return
		}
		T.serveAgent(w, r, user, agentKey, req)
		return
	}
	// Unprefixed. A caller that pastes a bare chat id or agent name means the
	// conversation, not a raw model — and silently answering from the worker
	// tier instead is the worst possible response: it WORKS, sounds plausible,
	// and quietly has no persona, no tools, no memory and no thread. (Seen
	// live: a voice platform configured with a raw iMessage chat id held a
	// whole conversation against the bare model.) Resolve it before falling
	// back, and log the fallback so the next one is visible.
	if target != "" && !isTierName(target) {
		if orch := findOrchestrate(); orch != nil {
			if tgt, ok := orch.ResolveExternalChannel(user, target); ok {
				if !gateTarget(w, user, token, "channel:"+tgt.ChatID) {
					return
				}
				Log("[openai_api] %s: model %q resolved as a CHANNEL (no prefix) — prefer \"channel:%s\"", user, target, target)
				T.serveChannel(w, r, user, target, req)
				return
			}
		}
		if id, ok := orchestrate.ResolveExternalAgent(T.DB, user, target); ok {
			if !gateTarget(w, user, token, "agent:"+id) {
				return
			}
			Log("[openai_api] %s: model %q resolved as an AGENT (no prefix) — prefer \"agent:%s\"", user, target, target)
			T.serveAgent(w, r, user, target, req)
			return
		}
		Log("[openai_api] %s: model %q matched no channel or agent — answering from the WORKER tier (no persona, tools, memory or thread). If you meant a conversation, check the id and that its agent has \"Reachable over MCP\" on.", user, target)
	}
	// Tier passthrough (worker/lead, or an unresolved bare name that falls back
	// to worker). Gate on the canonical tier.
	if !gateTarget(w, user, token, canonicalTier(target)) {
		return
	}
	T.servePassthrough(w, r, user, req)
}

// servePassthrough routes straight to a model tier. No tools, no memory, no
// agent loop — the caller's messages go to the model as-is and tokens stream
// back as they arrive. This is the path that can keep up with live voice.
func (T *OpenAIAPI) servePassthrough(w http.ResponseWriter, r *http.Request, user string, req chatReq) {
	msgs := make([]Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, Message{Role: m.Role, Content: textContent(m.Content)})
	}
	var opts []ChatOption
	if req.Temperature > 0 {
		opts = append(opts, WithTemperature(req.Temperature))
	}
	// Thinking off: a voice caller wants the answer, not a reasoning preamble,
	// and the tokens cost latency the caller pays for in dead air.
	opts = append(opts, WithThink(false))

	lead := strings.EqualFold(req.Model, "lead")
	call := T.WorkerChat
	if lead {
		call = T.LeadChat
	}

	if !req.Stream {
		resp, err := call(r.Context(), msgs, opts...)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "llm call failed: "+err.Error())
			return
		}
		writeJSON(w, completionEnvelope(req.Model, resp.Content, resp))
		return
	}

	sse := newSSE(w)
	if sse == nil {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported by this server")
		return
	}
	id := completionID()
	sse.chunk(id, req.Model, "", "assistant") // role-only opener, per the OpenAI stream shape
	llm := SharedWorkerLLM()
	if lead {
		if l := SharedLeadLLM(); l != nil {
			llm = l
		}
	}
	if llm == nil {
		sse.done()
		return
	}
	_, err := llm.ChatStream(r.Context(), msgs, func(delta string) {
		sse.chunk(id, req.Model, delta, "")
	}, opts...)
	if err != nil {
		// The stream may already have emitted text, so an error can't become an
		// HTTP status here — surface it as a final delta so the caller sees
		// SOMETHING rather than a silently truncated answer.
		sse.chunk(id, req.Model, "\n[error: "+err.Error()+"]", "")
	}
	sse.stop(id, req.Model)
	sse.done()
}

// serveAgent runs a full agent turn in a thread of its own — the agent's
// persona, tools, and memory, but a conversation private to this caller. Use
// channel: instead to land inside an existing room.
//
// Streams for real: AgentSyncRun.Stream wires the agent loop's token stream
// straight to the SSE writer, so first word out is first word generated. A turn
// that produces no deltas at all (tool-only rounds) still falls back to
// emitting the settled reply in sentence-sized chunks.
func (T *OpenAIAPI) serveAgent(w http.ResponseWriter, r *http.Request, user, agentKey string, req chatReq) {
	if agentKey == "" {
		writeErr(w, http.StatusBadRequest, "model \"agent:\" needs an agent id, e.g. agent:seed-chat")
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "the orchestrate app is not mounted, so agents can't be reached")
		return
	}
	resolved, ok := orchestrate.ResolveExternalAgent(T.DB, user, agentKey)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no agent %q reachable for this account — check the id, and turn on \"Reachable over MCP\" on that agent (the same switch governs this endpoint)", agentKey))
		return
	}
	input, system := turnInput(req)
	if input == "" {
		writeErr(w, http.StatusBadRequest, "no user message to answer")
		return
	}
	T.runAgentTurn(w, r, agentTurn{
		orch:  orch,
		owner: user,
		agent: resolved,
		// Per-caller thread: without a stable key every utterance would start a
		// new conversation and the agent would forget what it just said.
		session: sessionKey(r, req),
		sender:  callerName(r, req),
		input:   input,
		system:  system,
		model:   req.Model,
		stream:  req.Stream,
	})
}

// serveChannel runs the turn INSIDE an existing conversation: the agent bound to
// that chat, on that chat's own thread. This is the difference between "a voice
// call to my assistant" and "a voice call into the group chat" — the agent
// arrives already holding the room's history, and what it says on the call is
// part of that same thread afterward.
//
// The reply is NOT delivered back out to the room's transport. A caller
// speaking to the agent is not the same act as the agent posting to everyone,
// and sending on the room's behalf is exactly the kind of thing that should be
// deliberate rather than a side effect of picking a model string. The turn is
// recorded in the thread either way, so it shows up in gohort's transcript.
func (T *OpenAIAPI) serveChannel(w http.ResponseWriter, r *http.Request, user, chatKey string, req chatReq) {
	if chatKey == "" {
		writeErr(w, http.StatusBadRequest, "model \"channel:\" needs a chat id, handle, or room name — GET /v1/models lists the reachable ones")
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "the orchestrate app is not mounted, so channels can't be reached")
		return
	}
	tgt, ok := orch.ResolveExternalChannel(user, chatKey)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no reachable conversation %q — check GET /v1/models, and make sure the agent bound to that chat has \"Reachable over MCP\" on", chatKey))
		return
	}
	input, system := turnInput(req)
	if input == "" {
		writeErr(w, http.StatusBadRequest, "no user message to answer")
		return
	}
	T.runAgentTurn(w, r, agentTurn{
		orch:  orch,
		owner: user,
		agent: tgt.AgentID,
		// The room's own thread key — NOT a per-call id. Joining the thread is
		// the whole point; a fresh session here would give the agent the room's
		// memories but none of its conversation.
		session: tgt.SessionID,
		title:   tgt.Name,
		// Name the speaker so a group transcript reads as who-said-what instead
		// of attributing the call to the room at large.
		sender: callerName(r, req),
		input:  input,
		system: system,
		model:  req.Model,
		stream: req.Stream,
	})
}

// callerName labels the external speaker in the thread. A voice platform knows
// who is calling; without a hint the turn is still attributed to something
// honest rather than silently reading as the room itself.
func callerName(r *http.Request, req chatReq) string {
	if h := strings.TrimSpace(r.Header.Get("X-Caller-Name")); h != "" {
		return h
	}
	if req.User != "" {
		return req.User
	}
	return "Voice caller"
}

// turnInput picks the turn's input (the last user message) and the caller's
// system instructions out of an OpenAI message array.
func turnInput(req chatReq) (input, system string) {
	for _, m := range req.Messages {
		switch strings.ToLower(m.Role) {
		case "user":
			input = textContent(m.Content)
		case "system":
			if system == "" {
				system = textContent(m.Content)
			}
		}
	}
	return strings.TrimSpace(input), system
}

// agentTurn is one dispatch into an agent, however it was addressed.
type agentTurn struct {
	orch                                 *orchestrate.OrchestrateApp
	owner, agent, session, title, sender string
	input, system, model                 string
	stream                               bool
}

// runAgentTurn is the shared body behind the agent: and channel: routes.
//
// Streaming is LAZY on purpose. The SSE headers commit a 200, so opening the
// stream up front would make any pre-first-token failure — agent not found,
// run error, timeout — unreportable as an HTTP status; the caller would get a
// successful empty stream instead of an error it can act on. So the writer is
// created by the first delta, and until then failures return a real status.
func (T *OpenAIAPI) runAgentTurn(w http.ResponseWriter, r *http.Request, t agentTurn) {
	var (
		mu       sync.Mutex
		sse      *sseWriter
		id       = completionID()
		streamed strings.Builder
	)
	// emit is called from the LLM's streaming goroutine while this handler
	// blocks on the run, and again from this goroutine afterwards. The mutex
	// keeps those two from ever overlapping on the ResponseWriter.
	emit := func(delta string) {
		if delta == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if sse == nil {
			if sse = newSSE(w); sse == nil {
				return // no flusher; the non-streaming path below still answers
			}
			sse.chunk(id, t.model, "", "assistant")
		}
		streamed.WriteString(delta)
		sse.chunk(id, t.model, delta, "")
	}

	run := orchestrate.AgentSyncRun{
		AgentOwner:    t.owner,
		RuntimeUser:   t.owner,
		AgentKey:      t.agent,
		SubSessionID:  t.session,
		Message:       t.input,
		Title:         t.title,
		MessageSender: t.sender,
		// A real person is on the other end, so this is not a headless
		// agent-to-agent dispatch: no DELEGATED-INVOCATION preamble, and
		// follow-up questions are answerable.
		Interactive:          true,
		SystemPromptOverride: t.system,
	}
	// Thinking off. A caller on this endpoint is waiting on a response — often
	// out loud — and reasoning tokens are invisible to them: the stream stays
	// open, nothing audible arrives, and the platform hangs up. Measured: a
	// 19.75s silent stream against a 4096-token budget, killed by the caller.
	// The passthrough route has always done this; the agent routes now match.
	noThink := false
	run.Think = &noThink
	if t.stream {
		run.Stream = emit
	}

	ctx, cancel := context.WithTimeout(r.Context(), agentTurnTimeout)
	defer cancel()
	res, err := t.orch.RunAgentSyncContinuingRich(ctx, run)

	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		if sse == nil {
			writeErr(w, http.StatusBadGateway, "agent run failed: "+err.Error())
			return
		}
		// Already streaming — the status is spent, so the error has to ride
		// the stream rather than vanish into a truncated reply.
		sse.chunk(id, t.model, "\n[error: "+err.Error()+"]", "")
		sse.stop(id, t.model)
		sse.done()
		return
	}

	text := strings.TrimSpace(res.Text)
	if !t.stream || sse == nil {
		// Non-streaming, or a turn that produced no deltas at all (a tool-only
		// round, or a model that returned its answer in one shot).
		if text == "" {
			text = "(the agent produced no reply)"
		}
		if !t.stream {
			writeJSON(w, completionEnvelope(t.model, text, nil))
			return
		}
		if sse = newSSE(w); sse == nil {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported by this server")
			return
		}
		sse.chunk(id, t.model, "", "assistant")
		for _, part := range sentences(text) {
			sse.chunk(id, t.model, part, "")
		}
		sse.stop(id, t.model)
		sse.done()
		return
	}

	// Streamed. Emit whatever the settled reply holds beyond what already went
	// out: the loop can revise a round (a correction guard re-prompts, tool
	// prose gets dropped from the final text), so the deltas are not always a
	// prefix of the result. Only a clean extension is topped up — anything
	// else is left alone rather than re-speaking text the caller already heard.
	if rest, ok := strings.CutPrefix(text, streamed.String()); ok && strings.TrimSpace(rest) != "" {
		sse.chunk(id, t.model, rest, "")
	}
	sse.stop(id, t.model)
	sse.done()
}

// agentTurnTimeout bounds an agent turn so a wedged run can't hold a caller's
// connection open indefinitely. Generous relative to a chat turn because an
// agent that reaches for tools legitimately takes a while; a voice caller will
// usually give up long before this.
const agentTurnTimeout = 3 * time.Minute

// sessionKey picks the conversation thread for an agent run. A caller that
// supplies its own call id gets one continuing thread for that call; without
// one, every request would start fresh and the agent would have no memory of
// what it just said.
func sessionKey(r *http.Request, req chatReq) string {
	if h := strings.TrimSpace(r.Header.Get("X-Session-Id")); h != "" {
		return "ext:" + h
	}
	if req.Call.ID != "" {
		return "ext:" + req.Call.ID
	}
	if req.User != "" {
		return "ext:" + req.User
	}
	return "ext:default"
}

// sentences splits a reply into TTS-friendly chunks. Deliberately crude: the
// point is to hand a consumer speakable units in order, not to be a linguistic
// sentence splitter.
func sentences(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			if strings.TrimSpace(cur.String()) != "" {
				out = append(out, cur.String())
			}
			cur.Reset()
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

// --- response helpers --------------------------------------------------------

func completionID() string {
	return "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
}

func completionEnvelope(model, text string, resp *Response) map[string]any {
	out := map[string]any{
		"id":      completionID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	}
	if resp != nil {
		out["usage"] = map[string]any{
			"prompt_tokens":     resp.InputTokens,
			"completion_tokens": resp.OutputTokens,
			"total_tokens":      resp.InputTokens + resp.OutputTokens,
		}
	}
	return out
}

// sseWriter emits the OpenAI streaming shape: a series of
// chat.completion.chunk events terminated by a literal [DONE].
type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return &sseWriter{w: w, fl: fl}
}

func (s *sseWriter) chunk(id, model, delta, role string) {
	d := map[string]any{}
	if role != "" {
		d["role"] = role
	}
	if delta != "" {
		d["content"] = delta
	}
	s.emit(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": d}},
	})
}

func (s *sseWriter) stop(id, model string) {
	s.emit(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
}

func (s *sseWriter) emit(v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.fl.Flush()
}

func (s *sseWriter) done() {
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	s.fl.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers in the OpenAI error shape, which is what a client library
// will try to parse when a call fails.
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "invalid_request_error"},
	})
}

// findOrchestrate resolves the registered OrchestrateApp so agent-routed
// requests can dispatch. Cached after first hit.
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return cachedOrch
}
