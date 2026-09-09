package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// lastUserContent returns the .Content of the last user-role message
// in the LLM history, or "" when there isn't one. Used by the
// pre-plan knowledge search to query against the current user
// message before kicking off the orchestrator.
func lastUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// priorAssistantContext returns a continuity block for the worker —
// the user's immediately preceding turn (their question + the
// orchestrator's reply) so follow-ups like "tell me more" land with
// the right referent. Empty when this is the first turn or the
// session pointer isn't available.
//
// We pull from t.session.Messages (the in-memory mutable session
// the runner has been writing to), filter out the CURRENT user
// message (already the last entry), and surface the prior user +
// assistant exchange. Older history isn't included — the worker is
// step-focused, not conversation-aware; the orchestrator and the
// synthesis round handle deep history.
func (t *chatTurn) priorAssistantContext() string {
	if t == nil || t.session == nil {
		return ""
	}
	msgs := t.session.Messages
	// Last entry is the user's current message (handleSend appended
	// it). Look at what comes before that.
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs = msgs[:n-1]
	}
	if len(msgs) == 0 {
		return ""
	}
	var lastUser, lastAssistant string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && lastAssistant == "" {
			lastAssistant = strings.TrimSpace(msgs[i].Content)
			continue
		}
		if msgs[i].Role == "user" && lastUser == "" {
			lastUser = strings.TrimSpace(msgs[i].Content)
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	if lastAssistant == "" && lastUser == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Prior turn in this conversation\n")
	b.WriteString("Use this to resolve references like \"it\", \"that\", \"the X you mentioned\" in the user's new message.\n\n")
	if lastUser != "" {
		b.WriteString("**User previously asked:** ")
		b.WriteString(snippetOneLine(lastUser, 600))
		b.WriteString("\n\n")
	}
	if lastAssistant != "" {
		b.WriteString("**You replied:** ")
		b.WriteString(snippetOneLine(lastAssistant, 800))
		b.WriteString("\n\n")
	}
	return b.String()
}

// titleAfterFirstTurn fires a background goroutine that asks the
// worker LLM for a short, descriptive title given the first
// user/assistant exchange, then patches it onto the session. No-op
// when this isn't a fresh session or there's no LLM. Failures stay
// local — title generation is an optimization, not part of the
// reply contract.
//
// The goroutine takes its own context (background + timeout) since
// the parent request returns as soon as the reply is delivered;
// keeping the title work on the request's ctx would cancel it the
// moment handleSend returns.
func (t *chatTurn) titleAfterFirstTurn() {
	if t == nil || !t.isNewSession || t.app == nil || t.app.LLM == nil || t.session == nil {
		return
	}
	// Snapshot what the goroutine needs — the request's chatTurn /
	// session pointer don't outlive handleSend.
	llm := t.app.LLM
	udb := t.udb
	agentID := t.agent.ID
	sessID := t.session.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate.title] panicked for session=%s: %v", sessID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Re-load so the goroutine sees the persisted messages
		// (the parent already saved the assistant reply before
		// firing this).
		s, ok := loadChatSession(udb, agentID, sessID)
		if !ok {
			return
		}
		title := generateSessionTitle(ctx, llm, s)
		if title == "" || title == s.Title {
			return
		}
		s.Title = title
		_, _ = saveChatSession(udb, s)
		Log("[orchestrate.title] session=%s renamed: %q", sessID, title)
	}()
}

// emitStats pushes a per-message stats payload (tk/s, input/output/
// thinking tokens, elapsed) into the conversation pane so the user can
// see throughput per assistant turn. Mirrors the chat app's
// "12.3 tk/s · 1450 in · 230 out · 187 think · 18.7s" footer.
//
// Nil-safe: tool-only LLM rounds (no content output) get an empty
// payload and the framework skips rendering.
func (t *chatTurn) emitStats(msgID string, resp *Response, start time.Time) {
	if msgID == "" {
		return
	}
	elapsedMs := time.Since(start).Milliseconds()
	payload := map[string]any{
		"kind":       "stats",
		"id":         msgID,
		"elapsed_ms": elapsedMs,
	}
	usage := &ChatMessageUsage{ElapsedMs: elapsedMs}
	if resp != nil {
		// The prompt is the SUM. A provider reports input_tokens as the
		// uncached remainder only, so on a conversation whose system prompt and
		// history are a cache hit it is near zero — the turn looks free while
		// costing whatever a large prompt costs.
		promptTokens := resp.InputTokens + resp.CacheReadTokens + resp.CacheWriteTokens
		payload["input_tokens"] = promptTokens
		payload["cache_read_tokens"] = resp.CacheReadTokens
		payload["cache_write_tokens"] = resp.CacheWriteTokens
		payload["output_tokens"] = resp.OutputTokens
		payload["reasoning_tokens"] = resp.ReasoningTokens
		usage.InputTokens = promptTokens
		usage.CacheReadTokens = resp.CacheReadTokens
		usage.CacheWriteTokens = resp.CacheWriteTokens
		usage.OutputTokens = resp.OutputTokens
		usage.ReasoningTokens = resp.ReasoningTokens
		// Prefer the backend's per-phase throughput (llama.cpp) when
		// available — matches what the user sees in llama.cpp's own
		// UI. Fall back to a coarse output_tokens / elapsed otherwise.
		if resp.PredictedPerSecond > 0 {
			payload["tokens_per_sec"] = resp.PredictedPerSecond
			payload["prompt_per_sec"] = resp.PromptPerSecond
			usage.TokensPerSec = resp.PredictedPerSecond
			usage.PromptPerSec = resp.PromptPerSecond
		} else if resp.OutputTokens > 0 {
			elapsed := time.Since(start)
			if elapsed > 0 {
				rate := float64(resp.OutputTokens) / elapsed.Seconds()
				payload["tokens_per_sec"] = rate
				usage.TokensPerSec = rate
			}
		}
	}
	t.sse.Send(payload)
	t.lastUsageMu.Lock()
	t.lastUsage = usage
	t.lastUsageMu.Unlock()
}

// drainLastUsage returns the most-recent emitStats usage and clears
// the slot. Called from handleSend right before persisting an
// assistant ChatMessage so the saved record carries the stats footer.
// Returns nil when no stats event has fired (tool-only round, error
// before any LLM call) — callers omit Usage in that case.
func (t *chatTurn) drainLastUsage() *ChatMessageUsage {
	t.lastUsageMu.Lock()
	u := t.lastUsage
	t.lastUsage = nil
	t.lastUsageMu.Unlock()
	return u
}

// leadInMaxLen is the cutoff that separates a mid-round lead-in (one
// short sentence the model writes before a tool call — finalized as its
// own message bubble) from a full answer mis-emitted before a tool
// (cleared, so it doesn't double the answer the model then writes in its
// final reply). See the onStep !info.Done branch.
const leadInMaxLen = 600

// emitStatus pushes a phase-narration row into the activity pane.
// Mirrors servitor's status events ("Investigator: synthesizing…",
// "Verifying names…") — high-level signposts distinct from the plan
// block's per-step pips and the per-tool-call cmd/output rows.
//
// No bubble reset here. orchestrate locks the activity pane off, so
// the status text is invisible in this surface anyway; resetting
// currentMsgID would split consecutive tool calls across separate
// bubbles for an invisible event. Visible new cards (intent blocks
// at worker-step boundaries, message_done at round close) own the
// bubble reset explicitly.
func (t *chatTurn) emitStatus(text string) {
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "status",
		"id":   activityCheapID(),
		"text": text,
	})
}

func init() {
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: "tune_ack_timeout", Category: "Timeouts", Label: "Acknowledgment timeout", Help: "Bounds the fast \"On it…\" acknowledgment call.", Kind: KindSeconds, Default: 8, Min: 1, Max: 60})
}

// ackTimeout bounds the fast acknowledgment call. Short — the ack
// is only useful if it lands while the user is staring at dead air;
// a slow ack is worse than none.
func ackTimeout() time.Duration { return TuneDuration("tune_ack_timeout") }

// ackEnabled gates the concurrent "On it…" acknowledgment (see emitAck
// and its launch site). Off by default — on small llama.cpp slot pools
// the ack can't get a slot and is pure overhead; the "Thinking…" status
// already covers the dead air. Set true on a server with spare slots.
const ackEnabled = false

// emitAck fires a fast, no-think worker call that produces a short
// natural acknowledgment ("On it — checking that now.") and streams
// it as a status the moment it returns. Runs as a goroutine launched
// at turn start so it overlaps round-1 planning — the orchestrator's
// first round uses thinking mode, which delays its first visible
// output by seconds; this fills that gap with a contextual ack
// instead of a bare "Thinking…".
//
// The ack call itself decides whether an ack is warranted: greetings,
// thanks, and instantly-answerable questions get "NONE" back and emit
// nothing, so conversational turns don't get a needless "On it!".
func (t *chatTurn) emitAck(ctx context.Context, userMsg string) {
	if t == nil || t.app == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, ackTimeout())
	defer cancel()
	sys := "A user just messaged an assistant that may need tools, web search, or sub-agents to answer. In ONE short, natural sentence, acknowledge you're on it (e.g. \"On it — let me look that up.\" / \"Sure, checking now.\" / \"Give me a sec to dig into that.\"). Do NOT answer the request, do NOT ask questions, do NOT name tools or agents. If the message is a greeting, a thanks, or something you'd answer instantly with no lookup, reply with exactly NONE."
	resp, err := t.app.WorkerChat(cctx,
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sys), WithMaxTokens(30), WithThink(false),
		// Best-effort ack: never retry. The 8s ackTimeout is already
		// blown by the time it fails, so a retry just re-pays the wait
		// and spams "[retry] attempt failed" / "chat failed" for a call
		// whose result is optional. A failed ack is a silent no-op.
		WithMaxRetries(0),
	)
	if err != nil || resp == nil {
		return
	}
	ack := strings.TrimSpace(resp.Content)
	if ack == "" || strings.HasPrefix(strings.ToUpper(ack), "NONE") {
		return
	}
	// Strip wrapping quotes the model sometimes adds.
	ack = strings.Trim(ack, "\"'")
	t.emitStatus(ack)
}

// resolveTopic returns the turn's knowledge topic. With the per-turn
// classifier removed, this is just the value passed at chatTurn
// construction (sub-agent dispatches set it from the parent) or
// generalTopic. Kept as a method so callers don't reach into the
// field directly — leaves room to evolve scope without churning
// every call site.
func (t *chatTurn) resolveTopic() string {
	if t == nil || t.topic == "" {
		return generalTopic
	}
	return t.topic
}

// agentHasRetrievableContent reports whether the agent has any
// corpus the knowledge tools could surface this turn. Used to gate
// knowledge_search + fetch_knowledge_doc out of catalogs where they
// would only produce empty / not-found results — the LLM sees the
// tools, reaches for them, hallucinates doc_ids, and burns rounds.
// Considers:
//   - the agent's own AttachedCollections
//   - IngestAttachments (uploaded files land in the agent's per-(user,agent) bucket)
//   - active skills' AttachedCollections (active-path only)
//   - deployment-scope collections that auto-attach when the agent's
//     AttachedCollections list is empty (the open-pool default)
func (t *chatTurn) agentHasRetrievableContent() bool {
	if t == nil {
		return false
	}
	if len(t.agent.AttachedCollections) > 0 || t.agent.IngestAttachments {
		return true
	}
	for _, sk := range t.skillsActive {
		if len(sk.AttachedCollections) > 0 {
			return true
		}
	}
	// Open-pool default: when the agent has NO explicit collections,
	// deployment-scope collections auto-attach. If any exist, the agent
	// effectively has retrievable content.
	if len(t.agent.AttachedCollections) == 0 {
		for _, c := range ListCollections(nil, "") {
			if IsDeploymentScope(c) {
				return true
			}
		}
	}
	return false
}

// appendSearchOrderGuidance inserts a "knowledge before web" stub
// into the system prompt when the agent has both a knowledge corpus
// AND web tools enabled. Without this, most models default to
// web_search for any question they're unsure about, even when the
// agent's own corpus, skill-attached documents, or accumulated
// findings already have the answer. The stub reorders that default.
//
// Skipped when:
//   - the agent has no web tools in its catalog (no choice to reorder)
//   - the agent is Builder (its persona has its own search rhythm)
func (t *chatTurn) appendSearchOrderGuidance(sys string) string {
	return sys + searchOrderGuidanceBlock(t.agent, t.agentHasRetrievableContent())
}

// agentRecordHasRetrievableContent is the chatTurn-free approximation of
// agentHasRetrievableContent — the static signals readable off the record
// alone (own collections, uploaded-file ingestion, the deployment-scope
// auto-attach fallback). It can't see active skills' collections (a mid-turn
// state), which only means a skill-corpus-only agent misses this block on the
// channel path — harmless, vs. the old bug of PROMISING knowledge_search to
// agents that don't carry it.
func agentRecordHasRetrievableContent(agent AgentRecord) bool {
	if len(agent.AttachedCollections) > 0 || agent.IngestAttachments {
		return true
	}
	if len(agent.AttachedCollections) == 0 {
		for _, c := range ListCollections(nil, "") {
			if IsDeploymentScope(c) {
				return true
			}
		}
	}
	return false
}

// searchOrderGuidanceBlock is the chatTurn-free form so the shared capability
// assembler can render it for the channel/dispatch path too. Returns "" when
// the agent has no web tools (nothing to reorder), no retrievable corpus
// (hasCorpus — without it the block told the agent to call knowledge_search
// FIRST while the corpus gate kept that tool OUT of its catalog: a prompt
// naming a tool the agent doesn't carry, burning an unknown-tool round; prompt
// audit finding #6), or is Builder.
func searchOrderGuidanceBlock(agent AgentRecord, hasCorpus bool) string {
	if isBuilderAgent(agent.ID) || !hasCorpus {
		return ""
	}
	// Only fire when web tools are actually in the agent's effective
	// catalog — otherwise there's nothing to deprioritize. AllowedTools
	// empty = default pool (web tools likely present); otherwise check
	// the explicit list.
	hasWeb := len(agent.AllowedTools) == 0
	if !hasWeb {
		for _, name := range agent.AllowedTools {
			if name == "web_search" || name == "fetch_url" || name == "browse_page" {
				hasWeb = true
				break
			}
		}
	}
	if !hasWeb {
		return ""
	}
	return `

## Search order — knowledge first

Before reaching for web_search / fetch_url, ask: "Does this question require information that has CHANGED since my documents were written?" If no, call ` + "`knowledge_search`" + ` first — APIs don't change daily, runbooks haven't been edited, last year's policies haven't moved. Web is the exception. One cheap query beats an unnecessary web round.

If results from different documents disagree on a specific point, surface the conflict ("Doc A says X but Doc B says Y") rather than averaging or silently picking one.`
}

// facts loads the agent's structured key/value Explicit Memory entries
// (the always-in-prompt facts layer). Re-read every call — cheap and
// ensures store_fact mid-turn lands in subsequent worker steps.
// Gated on DisableExplicit; the framing of WHAT goes here is shaped
// by the agent's KnowledgeFraming.
func (t *chatTurn) facts() []MemoryFact {
	if t.agent.DisableExplicit {
		return nil
	}
	// Incognito is a clean room, and the promise on it is the user's own: they
	// opened the session from "+ New ▾" specifically to get "no baggage in,
	// nothing out" (see ChatSession.Incognito). Nothing sets that flag
	// programmatically — one assignment in the tree, straight from the request —
	// so honouring it here is carrying out an explicit instruction, not
	// overriding a default.
	//
	// The rule lives in this function because runPlan used to blank a local copy
	// instead, and a turn builds more than one prompt: the worker step and the
	// synthesis step each called facts() again and got everything back. The
	// clean room held for the orchestrator's prompt and for nothing downstream
	// of it. operatingNotes has always guarded here rather than at its callers;
	// its comment even claimed this function did the same.
	if t.incognitoSession() {
		return nil
	}
	return ListMemoryFacts(t.udb, factsNamespace(t.agent.ID))
}

// incognitoSession reports whether this turn runs in a clean room — the session
// a user opens from "+ New ▾" to get "no baggage in, nothing out" (see
// ChatSession.Incognito).
//
// ONE predicate, because the rule now governs both directions across five
// surfaces: the facts a prompt inherits, the operating notes beside them, and
// the three tools that would otherwise write or delete durable memory from
// inside the room. Written out five times it would be wrong in one of them
// within a release; that is the entire lesson of the audit this came out of.
func (t *chatTurn) incognitoSession() bool {
	return t.session != nil && t.session.Incognito
}

// refuseDurableMemoryInCleanRoom is the single refusal for a durable-memory
// write or delete attempted in an incognito session.
//
// A refusal the model can READ, not a silent no-op. A tool that quietly does
// nothing and reports success is how an agent comes to tell someone it saved
// something it did not. Withholding the tool instead is no better: the prompt
// still describes the capability, and an absent tool gets improvised around
// rather than reported. So the tool stays mounted and explains itself.
func (t *chatTurn) refuseDurableMemoryInCleanRoom(verb, because string) error {
	return fmt.Errorf("%s: this is an incognito (clean-room) session, where %s. Durable memory is neither read into this conversation nor written out of it — that is what the user asked for when they opened it, not a fault to route around. Say so plainly instead of retrying; if it should persist, they can tell you again in an ordinary session.", verb, because)
}

// gatedPersona returns the agent's persona prompt with any
// `<!-- @requires-tools: ... -->` sections stripped when the
// listed tools aren't in the effective tool set. Empty AllowedTools
// = no restriction (nil sentinel → strip nothing). When AllowedTools
// is explicit, the gate set is (framework always-on tools) +
// (agent's allowlist) so framework-provided things like plan_set
// and store_fact stay visible regardless of what the admin trimmed.
func (t *chatTurn) gatedPersona(prompt string) string {
	return gatedPersonaFor(t.agent, prompt)
}

// gatedPersonaFor is gatedPersona without a turn, so the dispatch prompt
// builder can gate a target it was handed as a record. The dispatch path used
// to skip gating entirely — it passed OrchestratorPrompt raw — which meant an
// agent reached over a channel carried persona sections for tools it does not
// have, and shipped the raw `@requires-tools` marker comments to the model
// besides.
func gatedPersonaFor(agent AgentRecord, prompt string) string {
	if len(agent.AllowedTools) == 0 {
		return StripPromptSectionsForTools(prompt, nil)
	}
	gate := append(frameworkAlwaysOnToolNames(), agent.AllowedTools...)
	return StripPromptSectionsForTools(prompt, gate)
}
