// dispatch_agent — lets the chat's LLM delegate work to an Agency
// (orchestrate) agent. Async-only: returns immediately; the agent's
// answer arrives in the chat as a separate message when it's done.
// Right for text-message UX — the user gets a quick ack and the
// answer later, rather than dead air while the agent runs. The chat
// stays responsive: a follow-up while the dispatch runs is injected
// into it (RouteInject), a follow-up after it finishes continues the
// thread (RoutePromote), and "/stop" cancels it mid-flight.
//
// There is intentionally no synchronous variant. The "I need the
// result to write my reply" case is handled by the async completion
// re-trigger — when the dispatch finishes, the main LLM composes the
// reply from the result. For texting that's strictly better than
// blocking: the user sees a quick ack, then the answer.
//
// Per-chat allowlist (Conversation.AllowedAgents) gates dispatch:
// only agents the admin has explicitly granted to THIS chat are
// reachable.

package phantom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// dispatchAgentTimeout caps the dispatch context used by the
// promotion path (a follow-up routed synchronously through an idle
// sub-agent). Generous-but-bounded so a follow-up reply doesn't sit
// pending forever when an agent gets stuck.
const dispatchAgentTimeout = 2 * time.Minute

// dispatchAgentAsyncTimeout caps fire-and-forget dispatches. Larger
// than the promotion cap since async work is allowed to be slow —
// the user isn't waiting on the chat reply. Still bounded so a
// runaway agent eventually surfaces an error to the chat instead of
// going silent.
const dispatchAgentAsyncTimeout = 15 * time.Minute

// inflightDispatchCancels registers the cancel func for each running
// async dispatch, keyed by SubSessionID. A "/stop" command looks up
// the chat's active sub-sessions (ActiveSubSessionsFor) and calls
// each cancel to tear the dispatch down mid-flight — phantom's
// equivalent of orchestrate's Cancel button. Entries are removed
// when the goroutine exits.
var inflightDispatchCancels sync.Map // subSessID -> context.CancelFunc

// cancelDispatch cancels a running dispatch by SubSessionID. Returns
// true if a live dispatch was found and canceled.
func cancelDispatch(subSessID string) bool {
	if v, ok := inflightDispatchCancels.Load(subSessID); ok {
		if cancel, ok := v.(context.CancelFunc); ok {
			cancel()
			return true
		}
	}
	return false
}

// phantomSubSessionID picks the canonical SubSession storage key for
// a phantom dispatch. Stable per (chat, target) so repeat dispatches
// + promotion follow-ups all land on the same lifecycle record and
// the same persisted message history.
func phantomSubSessionID(chatID, agentKey string) string {
	return "phantom-disp:" + chatID + ":" + agentKey
}

// isCancelIntent reports whether a message should be treated as a
// request to stop a running dispatch. Matches "/stop" / "/cancel"
// commands, or a bare "stop" / "cancel" / "never mind" sent as the
// entire message. Caller only acts on it when a dispatch is actually
// active — otherwise these words fall through as normal content (so
// "stop by the store on the way home" doesn't trip it: it's only the
// whole-message forms that count).
func isCancelIntent(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	switch t {
	case "/stop", "/cancel", "stop", "cancel", "never mind", "nevermind", "stop it", "cancel that":
		return true
	}
	return false
}

// forwardToDispatchToolDef lets the persona pass a RELEVANT interim
// update to a task it dispatched that's still running — instead of the
// framework auto-swallowing every message into the dispatch. The LLM
// decides what's relevant; unrelated chatter just gets a normal reply.
func (T *Phantom) forwardToDispatchToolDef(chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "forward_to_dispatch",
			Description: "Forward a RELEVANT update to a task you dispatched to a specialist agent that's still running in the background. Use when the user adds something pertinent to that running task (\"oh, also check X\", \"make it Tuesday instead\", a correction). The note is handed to the agent between its rounds. Forward ONLY things relevant to the running task — for unrelated messages just reply normally and DON'T call this. No-op if nothing is running.",
			Parameters: map[string]ToolParam{
				"note": {Type: "string", Description: "The update to pass to the running agent, phrased as added context/instruction for its task."},
			},
			Required: []string{"note"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			note := strings.TrimSpace(stringArgPhantom(args, "note"))
			if note == "" {
				return "", errors.New("note is required")
			}
			active := ActiveSubSessionsFor(chatID)
			if len(active) == 0 {
				return "Nothing is running right now, so there's no task to forward to.", nil
			}
			forwarded := 0
			for _, s := range active {
				if q := LookupSubSessionInjectionQueue(s.SubSessionID); q != nil {
					q.Push(note)
					forwarded++
				}
			}
			Log("[phantom] forward_to_dispatch chat=%s → %d active dispatch(es)", chatID, forwarded)
			return fmt.Sprintf("Forwarded to the running task (%d active).", forwarded), nil
		},
	}
}

// dispatchAgentToolDef is the DEFAULT (async, fire-and-forget)
// dispatch tool. Handler validates the allowlist, kicks off a
// background goroutine, and returns immediately with a "queued"
// confirmation. When the goroutine finishes, the result (or failure
// notice) is enqueued into the chat's outbox as a separate
// assistant message, so the user gets the answer when it arrives
// without anyone waiting on the synchronous reply path.
//
// chatID is the delivery target (where the eventual outbox message
// lands); handle is the recipient for the outbox item. Both come
// from the active conv at call time.
//
// Async-only: there's no synchronous sibling. The "I need the result
// to write my reply" case is covered by the completion re-trigger —
// when the dispatch finishes, the main LLM composes the reply from
// the result.
func (T *Phantom) dispatchAgentToolDef(db Database, chatID, handle string, conv Conversation, ownerUsername string) AgentToolDef {
	allowedSummary := summarizeAllowedAgents(db, ownerUsername, conv.AllowedAgents)
	desc := "When the user asks about something that fits a specialist agent's purpose, USE THIS instead of answering inline. Specialist agents have their own tools, memory, and voice tuned to their specialty — they produce better answers for the work they're built for than the chat persona ad-libbing. Returns immediately; the agent's answer arrives in this chat as a separate message when it's done (right for the texting cadence — user gets a quick ack now, the answer later). The agent doesn't see this chat's history, so make the brief self-contained.\n\nAfter calling this, REPLY TO THE USER WITH A SHORT, NATURAL ACKNOWLEDGMENT — \"On it.\" / \"Looking that up for you.\" / \"Standby — checking now.\" DO NOT recap what tool you called, do not name the agent, do not explain that something is running in the background. Treat it like a text reply where the person just said \"I'll go look\" — they wouldn't narrate the mechanics."
	if allowedSummary != "" {
		desc += "\n\nAgents you can dispatch to in this chat:\n" + allowedSummary
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "dispatch_agent",
			Description: desc,
			Parameters: map[string]ToolParam{
				"agent": {
					Type:        "string",
					Description: "Name (or id) of the agent to dispatch to. Must match one of the agents listed in the description.",
				},
				"brief": {
					Type:        "string",
					Description: "What you want the agent to do, phrased as if the user were asking it directly. The agent doesn't see this chat's history — be self-contained. Include any context the user provided that the agent needs.",
				},
				"fresh_session": {
					Type: "boolean",
					Description: "OPTIONAL (default false). Wipes the prior dispatch session with this agent for this chat BEFORE running — the agent starts with NO inherited history of past dispatches. Set true when the user's question is a NEW thread (topic shift, returning user with a different need, \"hey can you check…\", \"new question\", or a meaningful gap since the prior dispatch). Leave false (the default) when the user is following up on the agent's last answer (\"tell me more\", \"go deeper on point 3\", \"what about X\"). Wrong choice has real cost: setting it true on a follow-up loses the context the user assumed you remembered; leaving it false on a new thread compounds stale context across topics and reduces answer quality over time. When in doubt, prefer true — the safety margin is higher than the continuity gain. The wipe is irreversible.",
				},
			},
			Required: []string{"agent", "brief"},
		},
		Handler: func(args map[string]any) (string, error) {
			key := strings.TrimSpace(StringArg(args, "agent"))
			brief := strings.TrimSpace(StringArg(args, "brief"))
			freshSession := false
			if v, ok := args["fresh_session"].(bool); ok {
				freshSession = v
			}
			if key == "" || brief == "" {
				return "", errors.New("agent and brief are required")
			}
			if ownerUsername == "" {
				return "", errors.New("dispatch is disabled: no admin user found in AuthDB")
			}
			if !isAgentAllowed(db, ownerUsername, conv.AllowedAgents, key) {
				return "", fmt.Errorf("agent %q is not in this chat's allowlist — only the agents listed in the tool description are reachable here", key)
			}
			orch := findOrchestrate()
			if orch == nil {
				return "", errors.New("orchestrate runtime not available")
			}
			runtimeUser := phantomDispatchRuntimeUser(conv.ChatID)
			subSessID := phantomSubSessionID(conv.ChatID, key)

			// Hard in-flight guard. Claim the (chat, agent) slot
			// ATOMICALLY before launching anything: whether the LLM fired
			// two dispatch_agent calls in one turn or re-dispatched on a
			// follow-up instead of forwarding, a second concurrent launch
			// would race two goroutines on the same SubSession history and
			// deliver duplicate answers. The cancel is created here (not in
			// the goroutine) so the LoadOrStore is the single source of
			// truth — no window where two callers both think they're first.
			// If the slot is already held, fold this brief into the running
			// dispatch as a forwarded note rather than starting over.
			ctx, cancel := context.WithTimeout(context.Background(), dispatchAgentAsyncTimeout)
			if _, alreadyRunning := inflightDispatchCancels.LoadOrStore(subSessID, cancel); alreadyRunning {
				cancel() // unused — the in-flight dispatch owns this slot
				if q := LookupSubSessionInjectionQueue(subSessID); q != nil {
					q.Push(brief)
					Log("[phantom/dispatch_agent_async] chat=%s agent=%q already in-flight — folded new brief into running dispatch", conv.ChatID, key)
					return "Already working on that — I passed your extra detail along to it. The answer's coming shortly; no need to dispatch again.", nil
				}
				Log("[phantom/dispatch_agent_async] chat=%s agent=%q already in-flight — ignored duplicate dispatch", conv.ChatID, key)
				return "Already on it — that agent is still working and will reply shortly. No need to ask again.", nil
			}

			// We own the slot now. Track in SubSession lifecycle. Async —
			// Mint marks active; the goroutine MarkSubSessionIdle's on
			// completion so the next user turn can be promoted into the
			// same agent.
			MintSubSession(SubSession{
				SubSessionID:  subSessID,
				HostSessionID: conv.ChatID,
				HostApp:       "phantom",
				AgentID:       key,
				AgentName:     key,
				OwnerUser:     ownerUsername,
				Mode:          SubSessionModeAsync,
				Status:        SubSessionActive,
			})
			// Register a per-SubSession injection queue so user
			// messages arriving while this dispatch is still running
			// can be pushed in mid-flight (handled by phantom's
			// processMessage RouteInject branch). Released after the
			// dispatch finishes — late-arriving notes after MarkIdle
			// belong on the next promotion turn, not this one.
			RegisterSubSessionInjectionQueue(subSessID, ownerUsername, key)
			Log("[phantom/dispatch_agent_async] chat=%s owner=%s runtime=%s → agent=%q brief_chars=%d QUEUED",
				conv.ChatID, ownerUsername, runtimeUser, key, len(brief))
			go func(agentKey, briefMsg string, fresh bool) {
				defer func() {
					if r := recover(); r != nil {
						Log("[phantom/dispatch_agent_async] panic dispatching %q: %v", agentKey, r)
						RetireSubSession(subSessID, "panic")
					}
					// Release the in-flight slot + cancel ctx (the /stop
					// branch looks the cancel up here) when the run exits.
					inflightDispatchCancels.Delete(subSessID)
					cancel()
					ReleaseSubSessionInjectionQueue(subSessID)
				}()
				out, err := orch.RunAgentSyncContinuing(ctx, ownerUsername, runtimeUser, agentKey, subSessID, SubSessionInjectionQueueKey(subSessID), briefMsg, fresh)

				// User-initiated cancel (via /stop) surfaces as a
				// context.Canceled error. The /stop handler already
				// acked the user ("Stopped.") and retired the
				// sub-session, so don't emit the "couldn't complete the
				// lookup" failure message or re-trigger the chat — just
				// exit quietly.
				if err != nil && errors.Is(ctx.Err(), context.Canceled) {
					Log("[phantom/dispatch_agent_async] agent=%q canceled by user", agentKey)
					return
				}

				// Direct-return pattern. The dispatched agent did the
				// careful research with tools and produced a faithful
				// answer; the worker-LLM summarize step compresses it
				// to chat size. Sending that DIRECTLY to the user (no
				// main-LLM re-render) avoids the canonical "LLM
				// hallucinates while paraphrasing a known-good answer"
				// failure mode — main LLM has no tools, no source
				// material, just the summary, and paraphrasing
				// reliably swaps numbers, drops sources, and adds
				// plausible details that weren't there.
				//
				// To preserve continuity, the delivered text is ALSO
				// stored as an assistant message in phantom's chat
				// history, with a trailing [#dispatch:<id>] marker.
				// The main LLM sees this on the NEXT turn — same
				// effect as if it had generated the reply itself,
				// minus the corruption. The marker is invisible to
				// the user (stripped before outbox delivery) but
				// visible in chat history; the persona instructs the
				// LLM to call recall_dispatch_result(<id>) when the
				// user asks for more detail on that thread.
				var deliveredText, storedText string
				if err != nil {
					Log("[phantom/dispatch_agent_async] agent=%q FAILED: %v", agentKey, err)
					RetireSubSession(subSessID, "error")
					deliveredText = "Couldn't pull that up. Want me to try a different angle?"
					storedText = deliveredText
				} else {
					// Mark idle so the next user message can be
					// promoted into this sub-agent for a sync
					// follow-up (no synthetic re-trigger needed).
					MarkSubSessionIdle(subSessID)
					// Strip markdown from the dispatched agent's
					// reply BEFORE we summarize or store it. Phantom's
					// delivery channels (SMS, voice, mobile bubbles)
					// don't render markdown; asterisks and hashes
					// either read literally to TTS or visually clutter
					// the chat. The agent persona prompts already
					// instruct plain text, but defensive strip catches
					// the cases where the LLM still emits formatting.
					raw := markdownToPlain(strings.TrimSpace(out))
					if raw == "" {
						deliveredText = "Looked, but nothing came back for that."
						storedText = deliveredText
					} else {
						// Summarize via worker LLM, store the raw
						// under a short ID so recall_dispatch_result
						// can fetch it later.
						summary := markdownToPlain(summarizeDispatchResult(T.LLM, agentKey, briefMsg, raw))
						resultID := newDispatchResultID(chatID, agentKey, briefMsg)
						storeDispatchResult(db, dispatchResult{
							ID:      resultID,
							ChatID:  chatID,
							Agent:   agentKey,
							Brief:   briefMsg,
							Raw:     raw,
							Summary: summary,
							Created: time.Now(),
						})
						Log("[phantom/dispatch_agent_async] agent=%q OK raw_chars=%d summary_chars=%d id=%s",
							agentKey, len(raw), len(summary), resultID)
						deliveredText = strings.TrimSpace(summary)
						// Stored history carries a trailing dispatch
						// marker so the main LLM can call
						// recall_dispatch_result(<id>) on a follow-up
						// turn ("tell me more about point 3"). The
						// marker doesn't ride to the user — outbox
						// gets deliveredText only.
						storedText = deliveredText + "\n\n[#dispatch:" + resultID + "]"
					}
				}

				// Store ONE assistant message in chat history (full
				// text + marker if applicable). Future main-LLM turns
				// will see this in their conversation context. Then
				// deliver the user-facing text — split into SMS-sized
				// chunks if it overshoots 1500 chars.
				storeMessage(db, PhantomMessage{
					ID:        now() + "-" + newID(),
					ChatID:    chatID,
					Role:      "assistant",
					Text:      storedText,
					Timestamp: now(),
				})
				// Defensive scrub: this text is delivered with no LLM
				// re-render (direct-return), so strip any internal marker
				// before it reaches the user. No-op on the normal path
				// (the marker lives on storedText, not deliveredText).
				deliveredText = scrubInternalScaffolding(deliveredText)
				T.rememberRecentReply(deliveredText)
				chunks := SplitMarkdownForDelivery(deliveredText, 1500)
				for _, c := range chunks {
					if c == "" {
						continue
					}
					enqueueOutbox(db, OutboxItem{
						ID:      newID(),
						ChatID:  chatID,
						Handle:  "",
						Text:    c,
						Type:    "reply",
						Created: now(),
					})
				}
			}(key, brief, freshSession)
			return "Queued. Reply with a short natural acknowledgment (\"On it.\" / \"Standby.\" / etc.) — do not explain the dispatch.", nil
		},
	}
}

// wrapAsyncDispatchResult takes a raw sub-agent reply (often a long
// structured report — markdown headers, citations, lists) and
// produces a text-message-friendly summary in the chat persona's
// voice. Single LLM call against the worker tier with thinking off;
// no tools, no history, just a focused reshape task.
//
// On wrap failure (LLM error, empty response) the raw text falls
// through truncated, so the user always gets the answer — wrapping
// is a polish step, not a hard requirement.
func (T *Phantom) wrapAsyncDispatchResult(conv Conversation, agentKey, brief, raw string) string {
	if T == nil || T.LLM == nil {
		return truncateForSMS(raw, 1500)
	}
	cfg := defaultConfig(T.DB)
	personaName := cfg.PersonaName
	if conv.PersonaName != "" {
		personaName = conv.PersonaName
	}
	if personaName == "" {
		personaName = "AI Assistant"
	}
	sysPrompt := fmt.Sprintf(
		"You are %s, replying via text message. You delegated a task to a specialist agent and just got the agent's full report back. Compose a SHORT text-message reply that delivers the answer naturally — like you went, looked it up, and are texting back what you found.\n\nRules:\n- Plain text only. No markdown, no headers, no bullet lists.\n- Lead with the punchline. If the user asked a question, answer it directly in the first sentence.\n- Keep it to 1-4 sentences for simple findings; up to a short paragraph for richer ones. NEVER paste the full report.\n- Do NOT say \"the agent found\" / \"the researcher reports\" / \"according to the report.\" You're answering the user yourself.\n- If the report says \"I couldn't find X,\" relay that plainly: \"Couldn't find anything on X.\"",
		personaName,
	)
	userPrompt := fmt.Sprintf(
		"User originally asked: %s\n\n---\n\nAgent's full report:\n\n%s\n\n---\n\nNow compose your short text reply summarizing this for them.",
		brief, raw,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: userPrompt}},
		WithSystemPrompt(sysPrompt),
		WithMaxTokens(800),
		WithRouteKey("app.phantom.dispatch_wrap"),
		WithThink(false),
	)
	if err != nil || resp == nil {
		Log("[phantom/dispatch_agent_async] wrap LLM failed for agent=%q err=%v — falling back to truncated raw", agentKey, err)
		return truncateForSMS(raw, 1500)
	}
	wrapped := strings.TrimSpace(resp.Content)
	if wrapped == "" {
		return truncateForSMS(raw, 1500)
	}
	return wrapped
}

// truncateForSMS is the fallback path when the wrap LLM is
// unavailable. Trims to ~n chars at a sentence boundary if possible
// so the user gets a coherent partial answer rather than nothing.
func truncateForSMS(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if idx := strings.LastIndexAny(cut, ".!?"); idx > n/2 {
		cut = cut[:idx+1]
	}
	return cut + "\n\n[Reply truncated for SMS; ask for details if you need more.]"
}

// buildAvailableAgentsPromptBlock renders the allowed agents as a
// system-prompt section. Same bullet list as the tool description's
// allowed-agents block, wrapped in directive copy so the LLM treats
// it as a delegation hint rather than just a reference list. Empty
// when no agents resolve (gracefully no-ops in the prompt assembly).
func buildAvailableAgentsPromptBlock(db Database, ownerUsername string, allowed []string) string {
	bullets := summarizeAllowedAgents(db, ownerUsername, allowed)
	if bullets == "" {
		return ""
	}
	return "\n\nSPECIALIST AGENTS AVAILABLE: when the user asks about something that fits a specialist's purpose below, DELEGATE via dispatch_agent instead of answering inline — they have their own tools, memory, and grounded sources and answer their specialty better than you will. Dispatch as your FIRST move on such a question, EVEN WHEN you could answer it yourself: a specialist's domain is exactly where answering from your own memory yields confident-wrong specifics. Don't say you'll check and then answer from memory instead — dispatch, then send a short natural ack. Answer directly ONLY for genuinely general chat (greetings, opinions, follow-ups already covered) — NOT for specifics that land in a listed specialist's domain.\n\n" + bullets + "\n"
}

// summarizeAllowedAgents builds the "Agents you can dispatch to"
// bullet list for the tool description. Names + first sentence of
// description so the LLM has enough to pick the right one without
// the catalog ballooning.
func summarizeAllowedAgents(db Database, ownerUsername string, allowed []string) string {
	if len(allowed) == 0 || ownerUsername == "" {
		return ""
	}
	orch := findOrchestrate()
	if orch == nil {
		return ""
	}
	agents := orch.AgentsForUser(ownerUsername)
	byID := map[string]orchestrate.AgentRecord{}
	byName := map[string]orchestrate.AgentRecord{}
	for _, a := range agents {
		byID[a.ID] = a
		byName[strings.ToLower(a.Name)] = a
	}
	var b strings.Builder
	for _, key := range allowed {
		a, ok := byID[key]
		if !ok {
			a, ok = byName[strings.ToLower(key)]
		}
		if !ok {
			continue
		}
		// Defense-in-depth: refuse to advertise sub-agents even if a
		// stale ID slipped onto the chat's AllowedAgents (handleAvailableAgents
		// filters them at picker time, but existing configs predate
		// that filter). Sub-agents are reachable only through their
		// parent at dispatch time.
		if a.OwnedBy != "" {
			continue
		}
		desc := firstSentence(a.Description)
		if desc != "" {
			fmt.Fprintf(&b, "- %s — %s\n", a.Name, desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", a.Name)
		}
	}
	return strings.TrimSpace(b.String())
}

// isAgentAllowed reports whether the given key (agent id or name) is
// in the chat's allowlist. Resolves names via the owner's store so a
// chat configured with agent IDs stays valid even if the LLM passes
// the name.
func isAgentAllowed(db Database, ownerUsername string, allowed []string, key string) bool {
	if len(allowed) == 0 || ownerUsername == "" || key == "" {
		return false
	}
	keyLow := strings.ToLower(strings.TrimSpace(key))
	orch := findOrchestrate()
	if orch == nil {
		return false
	}
	agents := orch.AgentsForUser(ownerUsername)
	idByName := map[string]string{}
	subAgentIDs := map[string]bool{}
	for _, a := range agents {
		idByName[strings.ToLower(a.Name)] = a.ID
		if a.OwnedBy != "" {
			subAgentIDs[a.ID] = true
		}
	}
	wantedID := key
	if id, ok := idByName[keyLow]; ok {
		wantedID = id
	}
	// Defense-in-depth: sub-agents are unreachable directly from phantom
	// regardless of what's on the allowlist. They route only through
	// their parent.
	if subAgentIDs[wantedID] {
		return false
	}
	for _, allowedKey := range allowed {
		if allowedKey == key || strings.EqualFold(allowedKey, key) {
			return true
		}
		if allowedKey == wantedID {
			return true
		}
	}
	return false
}

// findOrchestrate locates the registered OrchestrateApp instance.
// Cached after first hit since the registry doesn't change at
// runtime. Returns nil when orchestrate isn't loaded — the caller
// surfaces a clear error so the LLM doesn't loop on a missing dep.
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

// firstSentence extracts the first sentence-ish run of text — up to
// the first period+space, or the first newline, or 120 chars. Used
// to keep the tool description compact when an agent's blurb is
// multi-paragraph.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
