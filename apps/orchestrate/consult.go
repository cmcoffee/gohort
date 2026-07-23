package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Consultation: the worker keeps the loop and asks a stronger model ONE
// self-contained question.
//
// The usual escalation moves a whole turn to the lead tier, which hands the
// stronger model the entire job — the full tool catalog, the whole session
// history, and the requirement to emit a large structured tool call at the end
// of it. That is the shape that stalls: answering is what a lead is reliably
// good at, acting over a 65k-token prompt is not.
//
// A consult inverts it. The call carries the question and its evidence and
// NOTHING else — no tool catalog, no history, no plan state — so it costs a few
// thousand tokens instead of the turn's full prompt, and there is no long
// context for a weaker lead to lose track of. The answer comes back as a tool
// result and the worker, which is good at grinding, keeps driving.
//
// The answer is ADVICE, never fact. Both tiers have been confidently wrong
// about the same API in the same session; a consult raises the hit rate, it
// does not confer truth. Every path here labels it as advice and tells the
// caller to verify with a real call.

// consultMaxPerTurn caps how many consults one turn may spend. Past it the
// agent is thrashing rather than stuck on a specific unknown, and the
// failure-shape de-escalation is the better remedy. Not silent — the cap
// returns a message the model can act on.
const consultMaxPerTurn = 3

// ConsultRouteKey is the routing stage for one-shot advice calls. Registered in
// orchestrate.go with Default "lead" and NOT Private, so an admin controls it
// from the routing menu exactly like every other stage — including pinning
// consults to the worker for a fully-local build flow, or to
// "worker (thinking)" to keep deliberation without leaving the box.
const ConsultRouteKey = "app.orchestrate.consult"

// consultSystemPrompt is fixed and short. It says answer from the evidence,
// give the concrete shape, and admit when the evidence doesn't settle it —
// the three failure modes that make advice worse than none.
const consultSystemPrompt = `You are advising another AI agent in the middle of a task. It is stuck and has sent you one question plus the raw evidence it has.

Answer ONLY from the evidence given. Do not invent endpoints, field names, or behavior that is not in it.

Be concrete. If the question is about a request or response shape, give the exact field names and their exact nesting — a snippet beats a description. If it is about a failure, name the cause and the specific change that fixes it.

If the evidence does not settle the question, say so plainly and name what WOULD settle it (a specific doc page, a specific probe call). A confident wrong answer costs the agent more rounds than an honest "not enough information".

No preamble, no restating the question, no offers of further help. Answer and stop.`

// consultTool builds the consult tool. Available to any agent with authoring
// reach — the need (an unfamiliar API's request shape, a wall hit repeatedly)
// is not Builder-specific, and an agent that can author tools can hit it.
func consultTool(t *chatTurn) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "consult",
			Description: "Ask a stronger model ONE self-contained question and get its answer back. Call this when: you have hit the same API failure twice and varying the arguments isn't working; the docs are ambiguous about a request or response shape; or you are about to make your first authoring call against an API you have not built against before. Do NOT call it for anything you can settle by testing directly, and do not call it for general advice — it sees only what you paste, not your conversation. The answer is ADVICE: apply it, then VERIFY with a real call before telling the user anything works.",
			Parameters: map[string]ToolParam{
				"question": {
					Type:        "string",
					Description: "ONE specific question. Good: \"What is the exact POST /call body for a per-call first message?\" Bad: \"How do I use this API?\"",
				},
				"evidence": {
					Type:        "string",
					Description: "The raw material the answer depends on — the doc excerpt, the full error body, your current template. Paste it verbatim; do not summarize. The consulted model sees ONLY this, not your conversation or your tools.",
				},
				"tried": {
					Type:        "string",
					Description: "Optional. What you already attempted and how it failed, so the answer doesn't repeat it.",
				},
			},
			Required: []string{"question", "evidence"},
			Caps:     []Capability{CapNetwork},
		},
		Handler: func(args map[string]any) (string, error) {
			question := strings.TrimSpace(StringArg(args, "question"))
			evidence := strings.TrimSpace(StringArg(args, "evidence"))
			if question == "" {
				return "", fmt.Errorf("question is required — ask ONE specific question")
			}
			if evidence == "" {
				return "", fmt.Errorf("evidence is required — the consulted model sees ONLY what you paste here, not your conversation. Include the doc excerpt, error body, or template the answer depends on")
			}
			if tried := strings.TrimSpace(StringArg(args, "tried")); tried != "" {
				evidence += "\n\nAlready tried, and it failed:\n" + tried
			}
			advice, err := t.consult(question, evidence)
			if err != nil {
				return "", err
			}
			return "ADVICE (consulted — this is advice, not fact; apply it and VERIFY with a real call before reporting anything as working):\n\n" + advice, nil
		},
	}
}

// consult runs the one-shot advice call. Shared by the consult tool and the
// agent loop's failure-shape guard (wired through AgentLoopConfig.Consult), so
// both surfaces send the same shape of request and obey the same per-turn cap.
//
// LeadChat, not LeadChatWithTools: the consulted model answers, it does not
// dispatch. Passing a catalog would rebuild the very prompt this exists to
// avoid.
func (t *chatTurn) consult(question, evidence string) (string, error) {
	if t == nil {
		return "", fmt.Errorf("consultation unavailable")
	}
	// Cap first: over-consulting is a property of the turn, and the model
	// should get the same "you are retrying, not stuck" answer whether or not
	// a lead happens to be reachable.
	if t.consultCount >= consultMaxPerTurn {
		return "", fmt.Errorf("consultation limit reached for this turn (%d). You are retrying rather than stuck on a specific unknown — diagnose from what you already have, or tell the user plainly what is blocked and what you tried", consultMaxPerTurn)
	}
	if t.app == nil {
		return "", fmt.Errorf("consultation unavailable")
	}
	t.consultCount++

	msgs := []Message{
		{Role: "system", Content: consultSystemPrompt},
		{Role: "user", Content: "QUESTION\n" + question + "\n\nEVIDENCE\n" + evidence},
	}
	// Routed like every other stage: LeadChat honors the route key and
	// delegates to the worker when the admin has set this stage to worker.
	// Thinking comes from the ROUTE, never hardcoded here — forcing it would
	// silently override the "worker" vs "worker (thinking)" distinction the
	// routing menu exists to express. RouteThink returns nil for a lead-routed
	// stage, so this adds nothing in that case and the provider default stands.
	opts := []ChatOption{WithRouteKey(ConsultRouteKey)}
	if think := RouteThink(ConsultRouteKey); think != nil {
		opts = append(opts, WithThink(*think))
		if *think {
			if budget := RouteThinkBudget(ConsultRouteKey); budget != nil {
				opts = append(opts, WithThinkBudget(*budget))
			}
		}
	}
	resp, err := t.app.LeadChat(t.ctx, msgs, opts...)
	if err != nil {
		Log("[orchestrate.consult] call %d failed: %v", t.consultCount, err)
		return "", fmt.Errorf("consultation failed: %w", err)
	}
	advice := ""
	tier := "unknown"
	if resp != nil {
		advice = strings.TrimSpace(resp.Content)
		// Which tier actually SERVED it — a consult routed to lead can still
		// fall back to worker, and the log should say which answered.
		if resp.Tier == LEAD {
			tier = "lead"
		} else {
			tier = "worker"
		}
	}
	if advice == "" {
		Log("[orchestrate.consult] call %d returned an empty answer (tier=%s)", t.consultCount, tier)
		return "", fmt.Errorf("consultation returned no answer — proceed from what you already have")
	}
	// Logged, not silent: a consult that never earns its keep should be
	// visible as a line you can count, not an invisible cost.
	Log("[orchestrate.consult] call %d/%d served by tier=%s — q=%q answer=%d chars",
		t.consultCount, consultMaxPerTurn, tier, firstLineSnippet(question, 80), len(advice))
	t.turnDiag("consulted", "Asked a stronger model one question mid-task: "+firstLineSnippet(question, 120))
	return advice, nil
}
