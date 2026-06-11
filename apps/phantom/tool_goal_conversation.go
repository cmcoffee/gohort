// Operator-goal contact conversations: the phantom side of the Operator's
// converse_with_contact tool. The Operator hands phantom a GOAL aimed at a real
// contact ("arrange a plumber visit for Tuesday"); phantom runs the back-and-
// forth text exchange autonomously and reports the outcome back to the Operator
// when it's done or stuck. Nobody blocks waiting on the human — the whole loop
// is async:
//
//	StartGoalConversation (operator_link.go) sends the opener, mints an IDLE
//	  autonomous sub-session keyed to the contact's chat, and returns.
//	[contact replies, minutes/hours later]
//	handleHook -> processCoalesced -> processMessage -> ResolveDispatchRoute
//	  finds the idle autonomous sub-session -> RouteGoal -> runGoalTurn.
//	runGoalTurn runs ONE decision turn (send another message, or finish) and
//	  either re-suspends idle or wakes the Operator via finishGoalConversation.
//
// The contact conversation IS a normal phantom chat (the contact's chatID), so
// the transcript lives in the usual messageTable and we reuse the whole inbound
// path. The only new state is the goalConversation record (the goal text + the
// back-edge target + a turn budget) and the autonomous-kind sub-session that
// makes inbound replies route here instead of to phantom's owner-facing persona.

package phantom

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	// Lets admins route the goal-turn decision LLM (worker by default, like the
	// rest of phantom's autonomous work). Registered so it appears in the admin
	// routing panel rather than silently defaulting to lead.
	RegisterRouteStage(RouteStage{
		Key:     "app.phantom.goal",
		Label:   "Phantom — contact goal conversation",
		Default: "worker (thinking)",
		Group:   "Apps",
	})
}

const (
	// goalConversationTable holds one goalConversation per contact chat,
	// keyed by the contact's chatID so the inbound path can load it with the
	// chatID it already has in hand.
	goalConversationTable = "phantom_goal_conversations"

	// goalAgentID tags the autonomous sub-session so log lines / UI read
	// sensibly; the routing discriminator is SubSessionKindAutonomous, not
	// this string (core stays app-agnostic).
	goalAgentID = "operator-goal"

	// goalMaxTurns caps outbound messages in a single goal conversation. A
	// safety backstop against a runaway exchange — past this the task finishes
	// "stuck" and reports back rather than texting a real person indefinitely.
	goalMaxTurns = 12
)

// goal conversation statuses (stored on goalConversation.Status).
const (
	goalStatusRunning = "running"
	goalStatusDone    = "done"
	goalStatusStuck   = "stuck"
)

// goalConversation is the durable payload for an operator-goal contact
// conversation. The transcript is NOT duplicated here — it lives in the
// contact chat's normal messageTable; this carries only what the goal runner
// needs across the async gaps plus the back-edge target.
type goalConversation struct {
	TaskID         string    `json:"task_id"`
	ContactChatID  string    `json:"contact_chat_id"`
	Handle         string    `json:"handle"`
	Goal           string    `json:"goal"`
	OwnerUser      string    `json:"owner_user"`
	OperatorAgent  string    `json:"operator_agent"`  // back-edge target agent
	OperatorThread string    `json:"operator_thread"` // back-edge pinned session
	Status         string    `json:"status"`          // running | done | stuck
	Turns          int       `json:"turns"`           // outbound messages sent so far
	MaxTurns       int       `json:"max_turns"`       // safety cap
	LastOutboundAt time.Time `json:"last_outbound_at"`
	Created        time.Time `json:"created"`
}

// goalConvSubSessionID is the stable sub-session key for a contact chat's goal
// conversation. One per contact chat — a contact only ever has one live goal.
func goalConvSubSessionID(contactChatID string) string {
	return "phantom-goal:" + contactChatID
}

func loadGoalConversation(db Database, contactChatID string) (goalConversation, bool) {
	if db == nil || contactChatID == "" {
		return goalConversation{}, false
	}
	var gc goalConversation
	if db.Get(goalConversationTable, contactChatID, &gc) {
		return gc, true
	}
	return goalConversation{}, false
}

func saveGoalConversation(db Database, gc goalConversation) {
	if db == nil || gc.ContactChatID == "" {
		return
	}
	db.Set(goalConversationTable, gc.ContactChatID, gc)
}

// hasLiveGoalConversation reports whether a goal conversation is actively
// running for this chat. handleHook uses it to force-process the contact's
// replies (bypassing the gatekeeper + auto-reply gate) so they reach the goal
// runner — without this a contact who isn't auto-reply-enabled would be ignored.
func hasLiveGoalConversation(db Database, contactChatID string) bool {
	gc, ok := loadGoalConversation(db, contactChatID)
	return ok && gc.Status == goalStatusRunning
}

// runGoalTurn drives one decision for an operator-goal conversation when the
// contact replies. The sub-session is left IDLE between turns (it spends most
// of its life suspended waiting on the human); coalescing (processCoalesced)
// already serializes turns per chat, so there's no concurrent runner to guard
// against and no need to flip the record active.
func (T *Phantom) runGoalTurn(contactChatID, handle, text string, sub SubSession) {
	gc, ok := loadGoalConversation(T.DB, contactChatID)
	if !ok || gc.Status != goalStatusRunning {
		// Stale route (task already finished/retired) — let it lie; a later
		// reply falls through to the persona via RouteNone next time.
		return
	}

	// A message from the OWNER on the contact's thread (from_me / empty handle)
	// is the user steering the mission, NOT the contact replying. Stash it as a
	// note for the next genuine contact turn rather than answering it as if the
	// owner were the contact.
	if strings.TrimSpace(handle) == "" {
		if note := strings.TrimSpace(text); note != "" {
			if q := LookupSubSessionInjectionQueue(sub.SubSessionID); q != nil {
				q.Push(note)
				Log("[phantom/goal] task=%s owner steering note queued", gc.TaskID)
			}
		}
		return
	}

	// Drain any owner steering notes accumulated while we waited.
	var steer []string
	if q := LookupSubSessionInjectionQueue(sub.SubSessionID); q != nil {
		for _, n := range q.Drain() {
			if s := strings.TrimSpace(n.Text); s != "" {
				steer = append(steer, s)
			}
		}
	}

	transcript := T.goalTranscript(contactChatID)

	// The decision: send the next message, or finish (goal met / stuck). Two
	// tools, exactly one fires per turn (RoundAbortTools closes the round on
	// either). Handlers record the choice into closure state; we act on it
	// after the loop returns.
	var sendText, outcome, summary string
	sendTool := AgentToolDef{
		Tool: Tool{
			Name:        "send_to_contact",
			Description: "Send the next text message to the contact to move toward the goal. Use this when the exchange needs to continue — ask a question, confirm a detail, propose a time, answer something they asked. Keep it natural and concise, like a real text.",
			Parameters: map[string]ToolParam{
				"text": {Type: "string", Description: "The message to text the contact. Plain text, conversational, one or two short sentences."},
			},
			Required: []string{"text"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			sendText = strings.TrimSpace(StringArg(args, "text"))
			if sendText == "" {
				return "", fmt.Errorf("text is required")
			}
			return "queued", nil
		},
	}
	finishTool := AgentToolDef{
		Tool: Tool{
			Name:        "finish",
			Description: "End the conversation and report back to the user who set the goal. Use 'met' when the goal is accomplished (e.g. the appointment is booked, the answer is confirmed). Use 'stuck' when you can't make progress (the contact declined, went unresponsive on the key question, or the request can't be fulfilled). Provide a short summary of the outcome.",
			Parameters: map[string]ToolParam{
				"outcome": {Type: "string", Description: "Either 'met' (goal accomplished) or 'stuck' (cannot make progress)."},
				"summary": {Type: "string", Description: "A short, factual summary of what happened and the result — what was agreed, what's outstanding. This is what the user sees."},
			},
			Required: []string{"outcome", "summary"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			outcome = strings.ToLower(strings.TrimSpace(StringArg(args, "outcome")))
			summary = strings.TrimSpace(StringArg(args, "summary"))
			if outcome != "met" && outcome != "stuck" {
				outcome = "stuck"
			}
			return "reported", nil
		},
	}

	sys := T.goalSystemPrompt(gc, steer)
	user := "Conversation so far (newest last):\n\n" + transcript +
		"\n\nThe contact just sent the last message above. Decide the single next step: call send_to_contact with your reply, or call finish if the goal is met or you're stuck. Call exactly one tool."

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: user}}, AgentLoopConfig{
		SystemPrompt:    sys,
		Tools:           []AgentToolDef{sendTool, finishTool},
		MaxRounds:       2,
		GraceRounds:     0,
		RoundAbortTools: []string{"send_to_contact", "finish"},
		RouteKey:        "app.phantom.goal",
	})
	if err != nil {
		Log("[phantom/goal] task=%s decision LLM failed: %v — leaving idle for retry", gc.TaskID, err)
		return
	}

	switch {
	case outcome != "":
		status := goalStatusDone
		if outcome != "met" {
			status = goalStatusStuck
		}
		T.finishGoalConversation(gc, status, summary)
	case sendText != "":
		gc.Turns++
		gc.LastOutboundAt = time.Now()
		if gc.Turns >= gc.MaxTurns {
			Log("[phantom/goal] task=%s hit turn cap (%d) — finishing stuck", gc.TaskID, gc.MaxTurns)
			T.finishGoalConversation(gc, goalStatusStuck,
				"Reached the message limit without closing this out. Last thing I was about to send: "+sendText)
			return
		}
		saveGoalConversation(T.DB, gc)
		T.deliverGoalMessage(contactChatID, handle, sendText)
		Log("[phantom/goal] task=%s sent turn %d", gc.TaskID, gc.Turns)
		// Stay idle (already idle) — suspend until the contact replies again.
	default:
		// Model emitted neither tool (e.g. plain text). Don't text the contact
		// raw model chatter; leave idle and let the next reply re-drive.
		Log("[phantom/goal] task=%s no tool call this turn — leaving idle", gc.TaskID)
	}
}

// deliverGoalMessage stores an outbound goal message in the contact chat's
// history (so the next turn's transcript includes it) and enqueues it to the
// bridge outbox, SMS-chunked like every other phantom reply.
func (T *Phantom) deliverGoalMessage(contactChatID, handle, text string) {
	text = markdownToPlain(strings.TrimSpace(text))
	if text == "" {
		return
	}
	storeMessage(T.DB, PhantomMessage{
		ID: now() + "-" + newID(), ChatID: contactChatID,
		Role: "assistant", Text: text, Timestamp: now(),
	})
	for _, c := range SplitMarkdownForDelivery(text, 1500) {
		if c == "" {
			continue
		}
		enqueueOutbox(T.DB, OutboxItem{
			ID: newID(), ChatID: contactChatID, Handle: handle,
			Text: c, Type: "reply", Created: now(),
		})
	}
}

// finishGoalConversation closes the task and wakes the Operator with the
// outcome. Order matters for the duplicate-wake guard: persist the terminal
// status BEFORE firing the back-edge, so a second coalesced reply that races in
// sees Status != running and bails. RetireSubSession is idempotent.
func (T *Phantom) finishGoalConversation(gc goalConversation, status, summary string) {
	gc.Status = status
	saveGoalConversation(T.DB, gc)
	subID := goalConvSubSessionID(gc.ContactChatID)
	RetireSubSession(subID, "goal_"+status)
	ReleaseSubSessionInjectionQueue(subID)

	if summary == "" {
		summary = "(no summary provided)"
	}
	outcomeLabel := "goal met"
	if status != goalStatusDone {
		outcomeLabel = "stuck"
	}
	msg := fmt.Sprintf(
		"[GOAL CONVERSATION — %s]\nContact: %s\nGoal: %s\n\nOutcome: %s\n\n"+
			"React in this thread: report it to the user (notify_me if they should know on the go), take any follow-up action (which routes through the authorization queue), or just note it.",
		outcomeLabel, gc.Handle, gc.Goal, summary)

	orch := findOrchestrate()
	if orch == nil {
		Log("[phantom/goal] task=%s finished (%s) but orchestrate unavailable — cannot wake Operator", gc.TaskID, status)
		return
	}
	// Wake on a fresh goroutine so the contact chat's coalescing slot frees
	// immediately — the back-edge drives the SEPARATE operator-thread session.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := orch.RunAgentSyncContinuing(ctx, gc.OwnerUser, gc.OwnerUser, gc.OperatorAgent, gc.OperatorThread, "", msg, false); err != nil {
			Log("[phantom/goal] task=%s back-edge wake failed: %v", gc.TaskID, err)
		}
	}()
	Log("[phantom/goal] task=%s finished (%s) — Operator woken", gc.TaskID, status)
}

// goalSystemPrompt builds the persona/role framing for the goal runner. It
// speaks AS the owner's assistant texting a contact — natural, concise, on a
// single mission — not as the owner-facing chat persona.
func (T *Phantom) goalSystemPrompt(gc goalConversation, steer []string) string {
	cfg := defaultConfig(T.DB)
	ownerName := strings.TrimSpace(cfg.OwnerName)
	if ownerName == "" {
		ownerName = "the person you're helping"
	}
	var b strings.Builder
	fmt.Fprintf(&b,
		"You are texting a contact on behalf of %s to accomplish ONE specific goal. "+
			"You are NOT the contact and NOT %s — you're their assistant, handling this over text.\n\n"+
			"GOAL: %s\n\n"+
			"How to operate:\n"+
			"- Write like a real person texting: short, natural, plain text. No markdown, no formal email tone, no sign-offs every message.\n"+
			"- Move the goal forward each message. Ask one clear thing at a time; confirm specifics (dates, times, prices, addresses) explicitly.\n"+
			"- Stay strictly on this goal. Don't invent commitments %s didn't authorize, and don't share personal details beyond what the goal needs.\n"+
			"- When the goal is accomplished, call finish with outcome 'met' and a short summary of what was agreed.\n"+
			"- If the contact declines, goes unresponsive on the key question, or the request can't be met, call finish with outcome 'stuck' and say why.\n",
		ownerName, ownerName, gc.Goal, ownerName)
	if len(steer) > 0 {
		b.WriteString("\nThe user just added guidance for you to factor in:\n")
		for _, s := range steer {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	return b.String()
}

// goalTranscript renders the contact chat's recent history for the decision
// turn: "them" for the contact's messages, "me" for what we've sent.
func (T *Phantom) goalTranscript(contactChatID string) string {
	msgs := recentMessages(T.DB, contactChatID, 30)
	if len(msgs) == 0 {
		return "(no messages yet)"
	}
	var b strings.Builder
	for _, m := range msgs {
		who := "them"
		if m.Role == "assistant" {
			who = "me"
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", who, text)
	}
	return strings.TrimSpace(b.String())
}

// composeGoalOpener writes the first message phantom texts the contact to kick
// off the goal. Best-effort LLM call against the worker tier; on any failure it
// falls back to a plain templated opener so the conversation always starts.
func (T *Phantom) composeGoalOpener(gc goalConversation) string {
	fallback := "Hi — reaching out to sort something out: " + gc.Goal
	if T == nil || T.LLM == nil {
		return fallback
	}
	sys := T.goalSystemPrompt(gc, nil) +
		"\nThis is the FIRST message — the contact hasn't heard from you yet. Open naturally and get to the point. Output only the message text, nothing else."
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: "Write the opening text message to send the contact now."}},
		WithSystemPrompt(sys),
		WithMaxTokens(300),
		WithRouteKey("app.phantom.goal"),
	)
	if err != nil || resp == nil {
		Log("[phantom/goal] task=%s opener LLM failed: %v — using fallback", gc.TaskID, err)
		return fallback
	}
	opener := markdownToPlain(strings.TrimSpace(resp.Content))
	if opener == "" {
		return fallback
	}
	return opener
}
