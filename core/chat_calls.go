package core

import (
	"context"
	"fmt"
	"time"
)

// LeadChat calls the lead LLM and tallies token usage.
// If the lead LLM fails and a separate primary LLM is available, it falls
// back to the primary so the session can continue rather than aborting.
func (T *AppCore) LeadChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	if T.LeadDenied() {
		// The private pin binds — redirect to worker instead of escalating.
		return T.WorkerChat(ctx, messages, opts...)
	}
	// Honor routing config: if a route key was supplied via WithRouteKey
	// and the stage is configured for "worker", delegate transparently.
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	if probe.RouteKey != "" && !probe.TierResolved && !RouteToLead(probe.RouteKey) {
		if probe.Think == nil {
			if think := RouteThink(probe.RouteKey); think != nil {
				opts = append(opts, WithThink(*think))
				if *think && probe.ThinkBudget == nil {
					if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
						opts = append(opts, WithThinkBudget(*budget))
					}
				}
				Debug("[llm] %s routed to worker LLM (thinking=%v, routing config)", probe.RouteKey, *think)
			} else {
				// The WORKER default, which is ON — the same fallback WorkerChat
				// applies for the same key ("worker tier: thinking on by default;
				// callers that don't need thinking pass WithThink(false)").
				// This used to default OFF, so one route key produced opposite
				// thinking depending on which entry point the call arrived
				// through. Its justification was wrong too: RouteThink returns
				// nil for the values "lead" and "" as well as for a nil lookup,
				// and a private stage holding a stale "lead" reaches exactly
				// here.
				opts = append(opts, WithThink(true))
				Debug("[llm] %s routed to worker LLM (routing config, worker default thinking)", probe.RouteKey)
			}
		} else {
			Debug("[llm] %s routed to worker LLM (thinking=%v, call-site override)", probe.RouteKey, *probe.Think)
		}
		return T.WorkerChat(ctx, messages, opts...)
	}
	// A lead route carries a thinking preference too ("lead (thinking)"), and
	// callers that don't pre-apply it would otherwise escalate the tier while
	// silently dropping the reasoning the stage asked for. A call-site Think
	// still wins — this only fills the gap.
	if probe.Think == nil {
		if think := RouteThink(probe.RouteKey); think != nil {
			opts = append(opts, WithThink(*think))
			if *think && probe.ThinkBudget == nil {
				if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
					opts = append(opts, WithThinkBudget(*budget))
				}
			}
		}
	}
	lead := T.GetLeadLLM()
	start := time.Now()
	resp, err := lead.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	// fellBackToWorker tracks whether the tokens on resp actually
	// came from the worker LLM (fallback path). Matters for the
	// UsageTracker tier attribution — worker pricing, not lead.
	fellBackToWorker := false
	if err != nil && probe.NoTierFallback {
		// Pinned to the lead on purpose. Surfaced at Log level rather than
		// Debug: a pin that quietly answered from the worker is exactly what the
		// pin was set to stop, and the failure needs to be visible without
		// anyone having debug on.
		Log("[llm] lead chat failed after %s and this call is pinned to the lead — NOT falling back: %s",
			elapsed.Round(time.Millisecond), err)
		return nil, err
	}
	if err != nil && T.LeadLLM != nil && T.LLM != nil && LeadIsDistinct() {
		Debug("[llm] lead chat failed after %s: %s — falling back to primary", elapsed.Round(time.Millisecond), err)
		T.LeadFallback = true
		fellBackToWorker = true
		start = time.Now()
		resp, err = T.LLM.Chat(ctx, messages, opts...)
		elapsed = time.Since(start)
		if err != nil {
			Debug("[llm] primary fallback also failed after %s: %s", elapsed.Round(time.Millisecond), err)
			return nil, err
		}
		Debug("[llm] primary fallback completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	} else if err != nil {
		Debug("[llm] lead chat failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return nil, err
	} else if resp.OutputTokens == 0 && resp.Content == "" && T.LeadLLM != nil && T.LLM != nil && LeadIsDistinct() {
		// Lead returned empty output (possible safety filter) — fall back to primary.
		Debug("[llm] lead chat returned empty after %s (input: %d, thinking: %d) — falling back to primary", elapsed.Round(time.Millisecond), resp.InputTokens, len(resp.Reasoning))
		T.LeadFallback = true
		fellBackToWorker = true
		start = time.Now()
		resp, err = T.LLM.Chat(ctx, messages, opts...)
		elapsed = time.Since(start)
		if err != nil {
			Debug("[llm] primary fallback also failed after %s: %s", elapsed.Round(time.Millisecond), err)
			return nil, err
		}
		Debug("[llm] primary fallback completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	} else {
		Debug("[llm] lead chat completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	}
	// Track the response against the tier that actually served it.
	//
	// Falling back is not the only way a "lead" call is served by the worker.
	// GetLeadLLM returns T.LLM when no lead is configured, and LeadIsDistinct
	// is false when a lead IS set but resolves to the same model — in both
	// cases lead.Chat above ran the WORKER, succeeded, and skipped the two
	// fallback branches (each of which requires a distinct lead to exist). So
	// every escalation on a worker-only deployment was recorded as lead
	// tokens and priced at the lead rate. With the usual arrangement — a local
	// worker at no cost, a cloud lead that is anything but — that does not
	// shade the estimate, it invents the entire bill.
	servedByWorker := fellBackToWorker || !T.HasDistinctLead()
	if servedByWorker {
		if resp != nil {
			resp.Tier = WORKER
		}
		T.trackTokens(ctx, resp)
	} else {
		if resp != nil {
			resp.Tier = LEAD
		}
		T.trackLeadTokens(ctx, resp)
	}
	return resp, nil
}

// WorkerChat calls T.LLM.Chat and tallies token usage on T.Report.
// If the call includes a WithRouteKey option and the routing menu has a
// thinking override configured for that stage, it is applied here so
// direct worker-session calls respect the same setting as lead-routed calls.
func (T *AppCore) WorkerChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	// A deployment with no worker LLM configured is a real state — the
	// machine rehearsal already checks for it by hand — and every path that
	// forgot to check reached this line and segfaulted three frames down
	// from the call it was really about. An error names the missing piece;
	// a nil dereference names the last function to touch it.
	if T == nil || T.LLM == nil {
		return nil, Error("no worker LLM is configured")
	}
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	if probe.RouteKey != "" && probe.Think == nil {
		if think := RouteThink(probe.RouteKey); think != nil {
			opts = append(opts, WithThink(*think))
			probe.Think = think
			if *think && probe.ThinkBudget == nil {
				if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
					opts = append(opts, WithThinkBudget(*budget))
				}
			}
			Debug("[llm] %s worker thinking override: %v (routing config)", probe.RouteKey, *think)
		}
	}
	// Worker tier: thinking on by default. Callers that don't need thinking
	// (e.g. title generation) pass WithThink(false) explicitly.
	if probe.Think == nil {
		opts = append(opts, WithThink(true))
		probe.Think = func() *bool { v := true; return &v }()
	}
	// Dynamic think budget is now handled at the LLM client layer
	// (core/llm_openai.go applyDynamicThinkBudget) so every call
	// path — Chat, ChatStream, Session.ChatStream, WorkerChat —
	// gets the same correct sizing without duplicating logic here.
	// Callers can still override per-call via WithThinkBudget.
	start := time.Now()
	resp, err := T.LLM.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	if err != nil {
		Debug("[llm] chat failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	if resp != nil {
		resp.Tier = WORKER
	}
	Debug("[llm] chat completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	T.trackTokens(ctx, resp)
	return resp, nil
}

// WorkerChatWithCalc is like WorkerChat but includes the calculator tool.
// If the LLM calls the calculator, executes it and sends the result back
// for a final answer. Handles at most 3 rounds of tool calls.
//
// Works with both native tool calling and prompt-based fallback:
// - Native: passes Tool definitions via WithTools, handles ToolCall responses
// - Prompt-based: injects tool description into system prompt, parses <tool_call> tags
func (T *AppCore) WorkerChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	calc, ok := FindChatTool("calculate")
	if !ok {
		return T.WorkerChat(ctx, messages, opts...)
	}
	return chatToolLoop(ctx, T.WorkerChat, messages, calcKit(calc), 3, opts...)
}

// calcKit wraps the calculate chat tool as a one-tool kit for chatToolLoop.
func calcKit(calc ChatTool) []AgentToolDef {
	return []AgentToolDef{{
		Tool: Tool{
			Name:        calc.Name(),
			Description: calc.Desc(),
			Parameters:  calc.Params(),
		},
		Handler: chatToolHandler(calc),
	}}
}

// toolChatMaxRounds bounds the *WithTools loop. Reference-source tools can
// chain (search -> facts -> investigate), so it's roomier than calc's 3.
const toolChatMaxRounds = 6

// WorkerChatWithTools runs WorkerChat with an app-supplied tool kit (e.g. the
// per-item tools an attached reference source contributes — search, facts,
// live investigate), executing tool calls in a bounded loop until the model
// answers in text. An empty kit degrades to a plain WorkerChat.
func (T *AppCore) WorkerChatWithTools(ctx context.Context, messages []Message, kit []AgentToolDef, opts ...ChatOption) (*Response, error) {
	return chatToolLoop(ctx, T.WorkerChat, messages, kit, toolChatMaxRounds, opts...)
}

// LeadChatWithTools is WorkerChatWithTools on the lead LLM. Route config
// (WithRouteKey) is respected — a stage routed to "worker" still redirects.
func (T *AppCore) LeadChatWithTools(ctx context.Context, messages []Message, kit []AgentToolDef, opts ...ChatOption) (*Response, error) {
	return chatToolLoop(ctx, T.LeadChat, messages, kit, toolChatMaxRounds, opts...)
}

// chatToolLoop is the shared bounded tool loop behind the *WithCalc and
// *WithTools chat variants. call is the chat method used every round
// (T.LeadChat or T.WorkerChat) so the caller keeps its routing / privacy
// posture. Both dispatch styles are supported:
// - Native: kit definitions ride via WithTools, ToolCall responses execute.
// - Prompt-based: kit is described in the system prompt, <tool_call> tags parsed.
func chatToolLoop(ctx context.Context, call func(context.Context, []Message, ...ChatOption) (*Response, error), messages []Message, kit []AgentToolDef, maxRounds int, opts ...ChatOption) (*Response, error) {
	if len(kit) == 0 {
		return call(ctx, messages, opts...)
	}
	tools := make([]Tool, 0, len(kit))
	handlers := make(map[string]ToolHandlerFunc, len(kit))
	for _, def := range kit {
		tools = append(tools, def.Tool)
		handlers[def.Tool.Name] = def.Handler
	}

	// Pass native tool definitions -- the LLM layer will strip them if
	// native tools are disabled, and the prompt-based fallback below
	// will handle that case.
	nativeOpts := append(append([]ChatOption{}, opts...), WithTools(tools))

	// Also inject the tools into the system prompt for models without
	// native support. We append to whichever system prompt is already set.
	promptToolText := BuildToolPrompt(kit)

	history := make([]Message, len(messages))
	copy(history, messages)

	// Accumulate token counts across all inner calls so the returned
	// Response reflects total consumption — callers (and
	// Session.ChatWithCalc) that attribute tokens to a session see the
	// full tool-loop cost, not just the final turn.
	var cumInput, cumOutput int
	for round := 0; round < maxRounds; round++ {
		callOpts := nativeOpts
		// Inject prompt-based tool description into system prompt.
		// This is additive -- models with native support will use
		// the native path and ignore the text, models without will
		// see the prompt and use <tool_call> tags.
		callOpts = append(callOpts, appendSystemPrompt(promptToolText))

		resp, err := call(ctx, history, callOpts...)
		if err != nil {
			return resp, err
		}
		cumInput += resp.InputTokens
		cumOutput += resp.OutputTokens

		// Check for native tool calls first.
		if len(resp.ToolCalls) > 0 {
			history = append(history, Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
			var results []ToolResult
			for _, tc := range resp.ToolCalls {
				handler, ok := handlers[tc.Name]
				if !ok {
					results = append(results, ToolResult{ID: tc.ID, Content: "unknown tool: " + tc.Name, IsError: true})
					continue
				}
				result, runErr := safeInvoke(ctx, tc.Name, handler, tc.Args)
				if runErr != nil {
					results = append(results, ToolResult{ID: tc.ID, Content: runErr.Error(), IsError: true})
				} else {
					results = append(results, ToolResult{ID: tc.ID, Content: result})
				}
				Debug("[chat-tool] %s -> %d bytes", tc.Name, len(result))
			}
			history = append(history, Message{Role: "tool", ToolResults: results})
			continue
		}

		// Check for prompt-based <tool_call> tags.
		tc, preamble := ParsePromptToolCall(resp.Content, handlers)
		if tc == nil {
			resp.InputTokens = cumInput
			resp.OutputTokens = cumOutput
			return resp, nil
		}
		result, runErr := safeInvoke(ctx, tc.Name, handlers[tc.Name], tc.Args)
		var resultText string
		if runErr != nil {
			resultText = "Error: " + runErr.Error()
		} else {
			resultText = result
		}
		Debug("[chat-tool] %s -> %d bytes", tc.Name, len(resultText))

		if preamble != "" {
			history = append(history, Message{Role: "assistant", Content: preamble})
		}
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("Tool result: %s\n\nContinue your response using this result.", resultText)})
	}

	// Max rounds hit, do a final call without tools to force a text response.
	finalResp, finalErr := call(ctx, history, opts...)
	if finalResp != nil {
		finalResp.InputTokens += cumInput
		finalResp.OutputTokens += cumOutput
	}
	return finalResp, finalErr
}

// LeadChatWithCalc is like WorkerChatWithCalc but uses the lead LLM for every
// round. Route config (WithRouteKey) is respected — if the stage is set to
// "worker" or "worker (thinking)", LeadChat will redirect to the worker.
func (T *AppCore) LeadChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	calc, ok := FindChatTool("calculate")
	if !ok {
		return T.LeadChat(ctx, messages, opts...)
	}
	return chatToolLoop(ctx, T.LeadChat, messages, calcKit(calc), 3, opts...)
}

// ChatWithCalc dispatches to LeadChatWithCalc or WorkerChatWithCalc based on
// the session's tier. The tool-calling loop runs on the same LLM tier as the
// session so lead sessions (e.g. gemma4) use lead for arithmetic too.
func (s *Session) ChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	var resp *Response
	var err error
	if s.Tier == LEAD {
		resp, err = s.agent.LeadChatWithCalc(ctx, messages, opts...)
	} else {
		resp, err = s.agent.WorkerChatWithCalc(ctx, messages, opts...)
	}
	s.recordTokens(resp)
	return resp, err
}

// appendSystemPrompt returns a ChatOption that appends text to the existing
// system prompt rather than replacing it.
func appendSystemPrompt(extra string) ChatOption {
	return func(c *ChatConfig) {
		c.SystemPrompt += extra
	}
}

// ChatStreamWithReport calls T.LLM.ChatStream and tallies token usage on T.Report.
func (T *AppCore) ChatStreamWithReport(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	// Honor routing config on the STREAMING path the same way LeadChat does
	// on the non-streaming path. Without this, every streaming agent-loop
	// round silently ran on the worker regardless of its route stage — the
	// Builder-on-lead route (app.orchestrate.builder, Default "lead") was
	// defeated purely because the Builder chat turn streams tokens to the UI.
	// Decision mirrors RunAgentLoop's non-streaming branch: a route key that
	// resolves to the lead tier + a distinct lead LLM wired → stream from lead.
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	useLead := probe.RouteKey != "" && RouteToLead(probe.RouteKey) && T.HasDistinctLead()
	// An explicit per-call pin beats the stage, the same precedence
	// AgentLoopConfig.wantsLead applies on the non-streaming path. Without this
	// the two paths disagreed: a caller that pinned a tier got it when it took
	// the plain-Chat branch and silently lost it the moment it streamed — which
	// is every interactive turn. A machine phase naming "lead" was the case
	// that surfaced it. The privacy floor is unchanged: escalating still
	// requires HasDistinctLead(), which folds in LeadDenied().
	if probe.TierOverride != TierUnset {
		useLead = probe.TierOverride == LEAD && T.HasDistinctLead()
		// A pin that CANNOT be honored has to say so. Somebody set a resource to
		// the lead and got the worker; without this line the only evidence is the
		// answer being worse than expected, and the cause (no distinct lead is
		// configured at all, or the privacy pin denies one) is invisible from
		// every surface that would make them look. This is the guard dropping
		// something, and a guard that drops something leaves a breadcrumb.
		if probe.TierOverride == LEAD && !useLead {
			Log("[llm] route=%q pinned to LEAD but running on the WORKER — %s. Nothing is wrong with the pin; there is no lead to reach.",
				probe.RouteKey, leadUnavailableReason(T))
		}
	}

	llm := T.LLM
	tier := WORKER
	if useLead {
		llm = T.LeadLLM
		tier = LEAD
	}
	// Same gap-fill as LeadChat: the stage's thinking preference applies to a
	// lead route as much as a worker one, and a streaming caller that didn't
	// pre-apply it would escalate the tier without the reasoning.
	if useLead && probe.Think == nil {
		if think := RouteThink(probe.RouteKey); think != nil {
			opts = append(opts, WithThink(*think))
			if *think && probe.ThinkBudget == nil {
				if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
					opts = append(opts, WithThinkBudget(*budget))
				}
			}
		}
	}

	// What the cached prefix looks like going in, compared against the
	// previous call of this turn. Silent unless a caller labelled the turn
	// (WithPromptTurn), and silent when nothing moved.
	watchCfg := applyOpts("", 0, opts)
	WatchPromptPrefix(ctx, watchCfg.SystemPrompt, watchCfg.Tools)

	start := time.Now()
	resp, err := llm.ChatStream(ctx, messages, handler, opts...)
	elapsed := time.Since(start)

	// Lead fallback: if the lead stream produced NOTHING (errored before any
	// output, or came back empty — e.g. a safety filter), retry once on the
	// worker. Gated on "no output" so we never double-stream tokens the
	// handler already delivered mid-stream — a lead stream that emitted then
	// errored keeps its partial resp and surfaces the error below.
	if useLead && (resp == nil || (resp.OutputTokens == 0 && resp.Content == "")) {
		if err != nil {
			Debug("[llm] %s lead stream failed after %s: %s — falling back to worker", probe.RouteKey, elapsed.Round(time.Millisecond), err)
		} else {
			Debug("[llm] %s lead stream returned empty after %s — falling back to worker", probe.RouteKey, elapsed.Round(time.Millisecond))
		}
		T.LeadFallback = true
		tier = WORKER
		start = time.Now()
		resp, err = T.LLM.ChatStream(ctx, messages, handler, opts...)
		elapsed = time.Since(start)
	}

	if err != nil {
		Debug("[llm] stream failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	if resp != nil {
		resp.Tier = tier
	}
	label := "worker"
	if tier == LEAD {
		label = "lead"
	}
	Debug("[llm] %s stream completed in %s (input: %d, output: %d tokens)", label, elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	if tier == LEAD {
		T.trackLeadTokens(ctx, resp)
	} else {
		T.trackTokens(ctx, resp)
	}
	return resp, nil
}

// trackTokens adds the response's token counts to the report tallies.
// This is the WORKER-tier tracker — WorkerChat and similar primary-LLM
// paths call it. For lead-tier calls, use trackLeadTokens. The split
// matters for cost estimation since worker and lead models typically
// price very differently.
func (T *AppCore) trackTokens(ctx context.Context, resp *Response) {
	if resp == nil {
		return
	}
	if T.Report != nil {
		// The WHOLE prompt, not the uncached remainder — otherwise a cached
		// turn tallies a handful of tokens against a prompt of thousands.
		if n := resp.InputTokens + resp.CacheReadTokens + resp.CacheWriteTokens; n > 0 {
			T.Report.Tally("Input Tokens").Add(n)
		}
		if resp.OutputTokens > 0 {
			T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
		}
	}
	// Feed the process-wide UsageTracker so per-run Scope() callers see the
	// worker portion of consumption, and the request-scoped tracker when this
	// call came from an HTTP request — that's what makes the middleware's
	// per-request cost line report this request's own spend instead of the
	// process-wide window. A no-op when the reloadable handle already claimed
	// this response, which is the normal case in the running server; this path
	// is what covers an AppCore holding a raw LLM.
	RecordUsage(ctx, WORKER, resp)
}

// trackLeadTokens is the lead-tier counterpart to trackTokens. Called
// from LeadChat when the response came from the lead LLM. When
// LeadChat falls back to the primary LLM (LeadFallback path), callers
// should use trackTokens instead so the tokens get attributed to the
// worker tier that actually served them.
func (T *AppCore) trackLeadTokens(ctx context.Context, resp *Response) {
	if resp == nil {
		return
	}
	if T.Report != nil {
		// The WHOLE prompt, not the uncached remainder — otherwise a cached
		// turn tallies a handful of tokens against a prompt of thousands.
		if n := resp.InputTokens + resp.CacheReadTokens + resp.CacheWriteTokens; n > 0 {
			T.Report.Tally("Input Tokens").Add(n)
		}
		if resp.OutputTokens > 0 {
			T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
		}
	}
	// See trackTokens: a no-op when the lead handle already claimed it.
	RecordUsage(ctx, LEAD, resp)
}
