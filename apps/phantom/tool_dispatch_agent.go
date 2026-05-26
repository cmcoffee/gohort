// dispatch_agent / dispatch_agent_inline — lets the chat's LLM
// delegate work to an Agency (orchestrate) agent. Two surfaces:
//
//   - dispatch_agent (DEFAULT, async): returns immediately; the
//     agent's answer arrives in the chat as a separate message when
//     it's done. Right for text-message UX — the user gets a quick
//     ack and the answer later, rather than dead air while the agent
//     runs. This is what the LLM should reach for unless it needs
//     the result inline for its own reply.
//   - dispatch_agent_inline (sync): blocks the chat reply until the
//     sub-agent finishes, then returns the synthesized text as the
//     tool result. Use ONLY when the LLM needs the agent's answer
//     before writing its OWN reply ("based on the research, X is
//     true...") — anything else, prefer the async default.
//
// Per-chat allowlist (Conversation.AllowedAgents) gates BOTH surfaces:
// only agents the admin has explicitly granted to THIS chat are
// reachable, regardless of which surface the LLM picks.

package phantom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// dispatchAgentTimeout caps how long the SYNCHRONOUS dispatch can
// take. Generous-but-bounded so an iMessage reply doesn't sit
// pending forever when an agent gets stuck.
const dispatchAgentTimeout = 2 * time.Minute

// dispatchAgentAsyncTimeout caps fire-and-forget dispatches. Larger
// than the sync cap since async work is allowed to be slow — the
// user isn't waiting on the chat reply. Still bounded so a runaway
// agent eventually surfaces an error to the chat instead of going
// silent.
const dispatchAgentAsyncTimeout = 15 * time.Minute

// dispatchAgentInlineToolDef builds the SYNCHRONOUS dispatch tool —
// blocks the chat reply until the sub-agent finishes, returns the
// synthesized text as the tool result. Used only when the chat's
// LLM needs the agent's answer before writing its own reply. For
// the default "delegate and get back to me" pattern, use the async
// dispatchAgentToolDef sibling.
//
// ownerUsername scopes the agent LOOKUP (which fleet of agents the
// chat can address — typically the deployment admin's). The DISPATCH
// runs as a synthetic per-chat user (phantom:<chat_id>) so memory,
// facts, knowledge, and workspace state for the dispatched agent
// stay isolated from the owner's interactive Agency use of the same
// agent.
func dispatchAgentInlineToolDef(db Database, conv Conversation, ownerUsername string) AgentToolDef {
	allowedSummary := summarizeAllowedAgents(db, ownerUsername, conv.AllowedAgents)
	desc := "INLINE (synchronous) dispatch to an Agency agent. Blocks your reply until the agent finishes. Use ONLY when you need the agent's answer to write YOUR OWN reply — e.g. \"based on the research, my take is...\". For pure delegation (\"go do this and text me when done\") use dispatch_agent instead — it's the default and doesn't block. Capped at 2 minutes."
	if allowedSummary != "" {
		desc += "\n\nAgents you can dispatch to in this chat:\n" + allowedSummary
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "dispatch_agent_inline",
			Description: desc,
			Parameters: map[string]ToolParam{
				"agent": {
					Type:        "string",
					Description: "Name (or id) of the agent to dispatch to. Must match one of the agents listed in the description.",
				},
				"brief": {
					Type:        "string",
					Description: "What you want the agent to do, phrased as if the user were asking it directly. The agent doesn't see this chat's history — be self-contained.",
				},
			},
			Required: []string{"agent", "brief"},
		},
		Handler: func(args map[string]any) (string, error) {
			key := strings.TrimSpace(StringArg(args, "agent"))
			brief := strings.TrimSpace(StringArg(args, "brief"))
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
			ctx, cancel := context.WithTimeout(context.Background(), dispatchAgentTimeout)
			defer cancel()
			runtimeUser := phantomDispatchRuntimeUser(conv.ChatID)
			Log("[phantom/dispatch_agent_inline] chat=%s owner=%s runtime=%s → agent=%q brief_chars=%d",
				conv.ChatID, ownerUsername, runtimeUser, key, len(brief))
			out, err := orch.RunAgentSync(ctx, ownerUsername, runtimeUser, key, brief)
			if err != nil {
				return "", fmt.Errorf("dispatch failed: %w", err)
			}
			return out, nil
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
// The synchronous sibling lives at dispatchAgentInlineToolDef — pick
// that one when the chat's LLM needs the agent's answer to write
// its own reply.
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
			},
			Required: []string{"agent", "brief"},
		},
		Handler: func(args map[string]any) (string, error) {
			key := strings.TrimSpace(StringArg(args, "agent"))
			brief := strings.TrimSpace(StringArg(args, "brief"))
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
			Log("[phantom/dispatch_agent_async] chat=%s owner=%s runtime=%s → agent=%q brief_chars=%d QUEUED",
				conv.ChatID, ownerUsername, runtimeUser, key, len(brief))
			go func(agentKey, briefMsg string) {
				defer func() {
					if r := recover(); r != nil {
						Log("[phantom/dispatch_agent_async] panic dispatching %q: %v", agentKey, r)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), dispatchAgentAsyncTimeout)
				defer cancel()
				out, err := orch.RunAgentSync(ctx, ownerUsername, runtimeUser, agentKey, briefMsg)

				// Pattern B: store the raw report in side storage,
				// pass only a worker-LLM-generated SUMMARY back to
				// the chat LLM. Keeps chat history lean while
				// preserving the option to retrieve full detail via
				// recall_dispatch_result. Failure cases (LLM error,
				// empty result) skip storage and feed a short error
				// note directly to the synthetic input.
				var syntheticInput string
				if err != nil {
					Log("[phantom/dispatch_agent_async] agent=%q FAILED: %v", agentKey, err)
					syntheticInput = fmt.Sprintf(
						"[SYSTEM: async dispatch to agent %q failed. Tell the user briefly that you couldn't complete the lookup — don't expose the error details, just say something like \"Couldn't find anything for that\" or \"That lookup didn't work out.\" Error context (for your reference only, don't surface): %v]",
						agentKey, err,
					)
				} else {
					raw := strings.TrimSpace(out)
					if raw == "" {
						syntheticInput = fmt.Sprintf(
							"[SYSTEM: async dispatch to agent %q finished but returned no content. Tell the user briefly that the lookup found nothing.]",
							agentKey,
						)
					} else {
						// Summarize via worker LLM, store the raw
						// under a short ID so recall_dispatch_result
						// can fetch it later.
						summary := summarizeDispatchResult(T.LLM, agentKey, briefMsg, raw)
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
						syntheticInput = fmt.Sprintf(
							"[SYSTEM: async dispatch from agent %q is ready. The user is waiting on this — compose a natural reply delivering the key findings. Don't acknowledge background dispatch, don't name the agent, just answer their question. Text-message-sized (~1500 chars max). The full report is stored under dispatch id %q — if the user later asks for more detail, call recall_dispatch_result with that id to retrieve the full text.]\n\n--- DISPATCH SUMMARY ---\n%s",
							agentKey, resultID, summary,
						)
					}
				}

				Log("[phantom/dispatch_agent_async] agent=%q re-triggering processMessage with synthetic input (%d chars)", agentKey, len(syntheticInput))
				// Refresh the conv record in case anything changed
				// during the async wait (memory updates, etc.).
				var freshConv Conversation
				if db.Get(conversationTable, chatID, &freshConv) {
					if freshConv.ChatID == "" {
						freshConv.ChatID = chatID
					}
				} else {
					freshConv = conv
				}
				// shouldSend always true here — the user is waiting
				// on this answer, so we always emit a reply.
				T.processMessage(chatID, chatID, "", syntheticInput, freshConv, func() bool { return true })
			}(key, brief)
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
	return "\n\nSPECIALIST AGENTS AVAILABLE: when the user asks about something that fits a specialist's purpose below, DELEGATE to them via dispatch_agent instead of answering inline — they have their own tools + memory + voice and produce better answers for their specialty. For ordinary chat (greetings, follow-ups, general questions you can handle), answer directly without delegating.\n\n" + bullets + "\n"
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
	for _, a := range agents {
		idByName[strings.ToLower(a.Name)] = a.ID
	}
	wantedID := key
	if id, ok := idByName[keyLow]; ok {
		wantedID = id
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
