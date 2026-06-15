// Phantom side of the core PhantomLink seam: lets the Operator (and any other
// app) read this device's conversations and send messages through phantom's
// outbox, without importing phantom. core/phantom_link.go owns the interface.
//
// Single-tenant gate: phantom is one device owner today, so the link serves
// ONLY the bridge owner (the admin account phantomToolOwner resolves) and
// returns nothing for any other user. That keeps a non-owner user's Operator
// from reading or sending through this phantom even on a shared instance. When
// phantom goes multi-tenant this gate compares against the per-bridge key
// owner instead; nothing on the Operator side changes.

package phantom

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

type phantomLinkImpl struct{ app *Phantom }

// registerOperatorLink wires phantom into the core PhantomLink registry. Called
// from RegisterRoutes once T.DB is set.
func (T *Phantom) registerOperatorLink() {
	RegisterPhantomLink(phantomLinkImpl{app: T})
}

// ownsBridge gates every link call to the single device owner.
func (p phantomLinkImpl) ownsBridge(owner string) bool {
	return owner != "" && owner == phantomToolOwner(p.app.DB)
}

func (p phantomLinkImpl) ListChats(owner string, limit int) ([]PhantomChatSummary, error) {
	if !p.ownsBridge(owner) {
		return nil, nil
	}
	var out []PhantomChatSummary
	for _, k := range p.app.DB.Keys(conversationTable) {
		var c Conversation
		if !p.app.DB.Get(conversationTable, k, &c) || c.AliasOf != "" {
			continue
		}
		s := PhantomChatSummary{ChatID: c.ChatID, Handle: c.Handle, DisplayName: c.DisplayName}
		if msgs := recentMessages(p.app.DB, c.ChatID, 1); len(msgs) > 0 {
			if t, err := time.Parse(time.RFC3339, msgs[len(msgs)-1].Timestamp); err == nil {
				s.LastAt = t
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (p phantomLinkImpl) ReadChat(owner, chatID string, limit int) ([]PhantomChatMessage, error) {
	if !p.ownsBridge(owner) {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	var out []PhantomChatMessage
	for _, m := range recentMessages(p.app.DB, chatID, limit) {
		at, _ := time.Parse(time.RFC3339, m.Timestamp)
		out = append(out, PhantomChatMessage{FromMe: m.Role == "assistant", Text: m.Text, At: at})
	}
	return out, nil
}

func (p phantomLinkImpl) SendToChat(owner, chatID, text string) error {
	if !p.ownsBridge(owner) {
		return fmt.Errorf("not authorized for this phantom bridge")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("message text is required")
	}
	var c Conversation
	p.app.DB.Get(conversationTable, chatID, &c)
	enqueueOutbox(p.app.DB, OutboxItem{
		ID: newID(), ChatID: chatID, Handle: c.Handle, Text: text, Type: "reply", Created: now(),
	})
	return nil
}

func (p phantomLinkImpl) SendToHandle(owner, handle, text string) error {
	if !p.ownsBridge(owner) {
		return fmt.Errorf("not authorized for this phantom bridge")
	}
	handle = strings.TrimSpace(handle)
	if handle == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("handle and text are required")
	}
	chatID := p.chatIDForHandle(handle)
	enqueueOutbox(p.app.DB, OutboxItem{
		ID: newID(), ChatID: chatID, Handle: handle, Text: text, Type: "reply", Created: now(),
	})
	return nil
}

func (p phantomLinkImpl) OwnerHandle(owner string) (string, bool) {
	if !p.ownsBridge(owner) {
		return "", false
	}
	cfg := defaultConfig(p.app.DB)
	h := strings.TrimSpace(cfg.OwnerHandle)
	return h, h != ""
}

// ResolveRecipient matches a loose recipient string to a conversation: exact
// chat_id wins, then an exact display-name match (primaries only), then a
// handle/alias match. A string that matches nothing is accepted ONLY when it's
// handle-shaped (phone/email) — a brand-new individual — otherwise it's an
// unresolvable name and the caller should surface the error rather than guess.
func (p phantomLinkImpl) ResolveRecipient(owner, to string) (PhantomChatSummary, bool) {
	if !p.ownsBridge(owner) {
		return PhantomChatSummary{}, false
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return PhantomChatSummary{}, false
	}
	var byName, byHandle *Conversation
	for _, k := range p.app.DB.Keys(conversationTable) {
		var c Conversation
		if !p.app.DB.Get(conversationTable, k, &c) {
			continue
		}
		if c.ChatID == to {
			return PhantomChatSummary{ChatID: c.ChatID, Handle: c.Handle, DisplayName: c.DisplayName}, true
		}
		if c.AliasOf != "" {
			continue // prefer primaries for name/handle matching
		}
		if byName == nil && c.DisplayName != "" && strings.EqualFold(c.DisplayName, to) {
			cc := c
			byName = &cc
		}
		if byHandle == nil {
			if strings.EqualFold(c.Handle, to) {
				cc := c
				byHandle = &cc
			} else {
				for _, a := range c.AliasHandles {
					if strings.EqualFold(a, to) {
						cc := c
						byHandle = &cc
						break
					}
				}
			}
		}
	}
	pick := byName
	if pick == nil {
		pick = byHandle
	}
	if pick != nil {
		return PhantomChatSummary{ChatID: pick.ChatID, Handle: pick.Handle, DisplayName: pick.DisplayName}, true
	}
	if looksLikeHandle(to) {
		return PhantomChatSummary{Handle: to}, true
	}
	return PhantomChatSummary{}, false
}

// looksLikeHandle reports whether a string is a plausible phone/email handle —
// the only kind of unknown recipient we'll start a brand-new thread to. Keeps a
// bare name ("WiWee") from being treated as a handle and creating a junk thread.
func looksLikeHandle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "@") || strings.HasPrefix(s, "+") {
		return true
	}
	return s[0] >= '0' && s[0] <= '9'
}

func (p phantomLinkImpl) DescribeChat(owner, chatID string) (PhantomChatSummary, bool) {
	if !p.ownsBridge(owner) || strings.TrimSpace(chatID) == "" {
		return PhantomChatSummary{}, false
	}
	var c Conversation
	if !p.app.DB.Get(conversationTable, chatID, &c) {
		return PhantomChatSummary{}, false
	}
	return PhantomChatSummary{ChatID: c.ChatID, Handle: c.Handle, DisplayName: c.DisplayName}, true
}

// DeliverMessage routes an Operator-originated message by whether phantom's
// persona is live for the target chat: persona on → the chat's LLM composes in
// voice/context; persona off → the text is sent verbatim. See the PhantomLink
// interface doc. Returns the text actually delivered.
func (p phantomLinkImpl) DeliverMessage(owner, chatID, handle, text string, images []string) (string, error) {
	if !p.ownsBridge(owner) {
		return "", fmt.Errorf("not authorized for this phantom bridge")
	}
	chatID = strings.TrimSpace(chatID)
	handle = strings.TrimSpace(handle)
	// Strip any phantom control markers the agent embedded in the message text
	// (e.g. [ATTACH: file.png, cleanup=true]). Those are an internal delivery
	// convention, not message content — without this they leak verbatim into
	// the recipient's SMS. The actual file(s) ride in `images`, resolved by the
	// caller from its own workspace.
	text = strings.TrimSpace(attachMarkerRe.ReplaceAllString(text, ""))
	if text == "" && len(images) == 0 {
		return "", fmt.Errorf("message text is required")
	}
	if chatID == "" && handle == "" {
		return "", fmt.Errorf("chat_id or handle is required")
	}
	if chatID == "" {
		chatID = p.chatIDForHandle(handle)
	}
	cfg := defaultConfig(p.app.DB)
	primary, active := p.app.chatPersonaActive(cfg, chatID)
	if handle == "" {
		handle = primary.Handle
	}
	if active {
		Log("[phantom/operator-deliver] chat=%s — persona ACTIVE, composing in voice (%d attachment(s))", chatID, len(images))
		return p.app.composeOperatorRelay(chatID, handle, primary, cfg, text, images), nil
	}
	// Persona off for this chat — send the Operator's words verbatim.
	Log("[phantom/operator-deliver] chat=%s — persona inactive, sending verbatim (%d attachment(s))", chatID, len(images))
	enqueueOutbox(p.app.DB, OutboxItem{
		ID: newID(), ChatID: chatID, Handle: handle, Text: text, Images: images, Type: "reply", Created: now(),
	})
	return text, nil
}

// chatPersonaActive reports whether phantom's persona answers this chat, using
// the SAME gate as the inbound path (handleHook): globally enabled AND
// (reply-to-all OR the chat's primary has auto-reply OR an alias of it does).
// Returns the effective PRIMARY conversation so the caller composes with the
// right persona/context. Re-deriving a simpler check here (only the one resolved
// record) diverges from how phantom actually behaves — a chat whose auto-reply
// was set on an alias address would wrongly read as inactive and get a raw send.
func (T *Phantom) chatPersonaActive(cfg PhantomConfig, chatID string) (Conversation, bool) {
	var c Conversation
	T.DB.Get(conversationTable, chatID, &c)
	primary := c
	aliasAutoReply := false
	if c.AliasOf != "" {
		// The targeted record is itself an alias — auto-reply may be set on it,
		// and the canonical settings live on the primary it points to.
		aliasAutoReply = c.AutoReply
		var pr Conversation
		if T.DB.Get(conversationTable, c.AliasOf, &pr) {
			primary = pr
		}
	}
	if !cfg.Enabled {
		return primary, false
	}
	if cfg.AutoReplyAll || primary.AutoReply || aliasAutoReply {
		return primary, true
	}
	// Auto-reply may have been enabled on a SIBLING alias address; phantom would
	// answer this conversation when a message arrives there, so the persona is
	// effectively active for the chat. Mirror that (handleHook sees it via the
	// incoming alias; outbound we must scan).
	pid := primary.ChatID
	if pid == "" {
		pid = chatID
	}
	for _, k := range T.DB.Keys(conversationTable) {
		var ac Conversation
		if T.DB.Get(conversationTable, k, &ac) && ac.AliasOf == pid && ac.AutoReply {
			return primary, true
		}
	}
	return primary, false
}

// StartGoalConversation kicks off an autonomous, multi-turn conversation with a
// contact toward a goal (see tool_goal_conversation.go for the runner). It mints
// the durable goalConversation record + an IDLE autonomous sub-session keyed to
// the contact's chat (so the contact's replies route to runGoalTurn, not the
// owner-facing persona), then sends the opening message and returns. Nobody
// blocks — the exchange is driven turn-by-turn as replies arrive.
func (p phantomLinkImpl) StartGoalConversation(owner, chatID, handle, goal, operatorAgent, operatorThread string) (string, error) {
	if !p.ownsBridge(owner) {
		return "", fmt.Errorf("not authorized for this phantom bridge")
	}
	chatID = strings.TrimSpace(chatID)
	handle = strings.TrimSpace(handle)
	goal = strings.TrimSpace(goal)
	if goal == "" || (chatID == "" && handle == "") {
		return "", fmt.Errorf("goal and a recipient (chat_id or handle) are required")
	}
	// Prefer the explicit chat id (unambiguous); fall back to resolving a
	// handle to its conversation only when no chat id was supplied.
	contactChatID := chatID
	if contactChatID == "" {
		contactChatID = p.chatIDForHandle(handle)
	}
	// For display/outbound addressing, recover the handle from the conversation
	// when we were given only a chat id.
	if handle == "" {
		var c Conversation
		if p.app.DB.Get(conversationTable, contactChatID, &c) {
			handle = c.Handle
		}
	}

	// One live goal per contact at a time — fold a re-request into the existing
	// one rather than starting a second runner on the same thread.
	if hasLiveGoalConversation(p.app.DB, contactChatID) {
		return "", fmt.Errorf("already running a conversation with %s — let it finish or stop it first", handle)
	}

	taskID := newID()
	gc := goalConversation{
		TaskID:         taskID,
		ContactChatID:  contactChatID,
		Handle:         handle,
		Goal:           goal,
		OwnerUser:      owner,
		OperatorAgent:  operatorAgent,
		OperatorThread: operatorThread,
		Status:         goalStatusRunning,
		Turns:          0,
		MaxTurns:       goalMaxTurns,
		LastOutboundAt: time.Now(),
		Created:        time.Now(),
	}
	saveGoalConversation(p.app.DB, gc)

	subID := goalConvSubSessionID(contactChatID)
	// Start IDLE (suspended, joinable): the contact's reply re-drives the
	// runner. Autonomous kind exempts it from promotion-window retirement and
	// keeps phantom's persona from ever answering the contact.
	MintSubSession(SubSession{
		SubSessionID:  subID,
		HostSessionID: contactChatID,
		HostApp:       "phantom",
		AgentID:       goalAgentID,
		AgentName:     "contact goal conversation",
		OwnerUser:     owner,
		Mode:          SubSessionModeAsync,
		Kind:          SubSessionKindAutonomous,
		Status:        SubSessionIdle,
	})
	RegisterSubSessionInjectionQueue(subID, owner, goalAgentID)

	opener := p.app.composeGoalOpener(gc)
	gc.Turns = 1
	gc.LastOutboundAt = time.Now()
	saveGoalConversation(p.app.DB, gc)
	p.app.deliverGoalMessage(contactChatID, handle, opener)

	Log("[phantom/goal] started task=%s chat=%s handle=%s goal_chars=%d", taskID, contactChatID, handle, len(goal))
	return taskID, nil
}

// composeOperatorRelay runs the chat's persona LLM to turn the Operator's
// intent into an in-voice outbound message, then stores + sends it. A focused
// single compose (not the full agentic loop) — no tools, no dispatch — so a
// relayed message can't trigger phantom's tool machinery. Falls back to sending
// the raw intent if the compose fails, so the message always goes out.
func (T *Phantom) composeOperatorRelay(chatID, handle string, conv Conversation, cfg PhantomConfig, intent string, images []string) string {
	deliver := func(text string) string {
		text = markdownToPlain(strings.TrimSpace(text))
		if text == "" {
			text = intent
		}
		T.rememberRecentReply(text)
		storeMessage(T.DB, PhantomMessage{
			ID: now() + "-" + newID(), ChatID: chatID, Role: "assistant", Text: text, Timestamp: now(),
		})
		// Attach the caller's images to the FIRST outgoing chunk; later chunks
		// are text-only. attached guards against duplicating attachments across
		// a multi-chunk split.
		attached := false
		for _, c := range SplitMarkdownForDelivery(text, 1500) {
			if c == "" {
				continue
			}
			item := OutboxItem{ID: newID(), ChatID: chatID, Handle: handle, Text: c, Type: "reply", Created: now()}
			if !attached {
				item.Images = images
				attached = true
			}
			enqueueOutbox(T.DB, item)
		}
		// Text was empty but there are attachments to send — emit an
		// image-only item so the file still goes out.
		if !attached && len(images) > 0 {
			enqueueOutbox(T.DB, OutboxItem{ID: newID(), ChatID: chatID, Handle: handle, Images: images, Type: "reply", Created: now()})
		}
		return text
	}
	if T.LLM == nil {
		return deliver(intent)
	}

	personaName := cfg.PersonaName
	if conv.PersonaName != "" {
		personaName = conv.PersonaName
	}
	personality := cfg.Personality
	if conv.Personality != "" {
		personality = conv.Personality
	}
	convRules := cfg.SystemPrompt
	if conv.SystemPrompt != "" {
		convRules = conv.SystemPrompt
	}
	basePrompt := buildSystemPrompt(personality, convRules)
	recipient := handle
	if conv.DisplayName != "" {
		recipient = fmt.Sprintf("%s (%s)", conv.DisplayName, handle)
	}

	var msgs []Message
	for _, m := range recentMessages(T.DB, chatID, 10) {
		role := "user"
		content := m.Text
		if m.Role == "assistant" {
			role = "assistant"
		} else {
			label := handle
			if conv.DisplayName != "" {
				label = conv.DisplayName
			}
			content = label + ": " + m.Text
		}
		msgs = append(msgs, Message{Role: role, Content: content})
	}
	// Put the intent on the LAST user turn (most salient position) and forbid
	// echoing. Without this, the prior delivered message sits as the last
	// assistant turn in the history above and a small model continues/repeats
	// it verbatim instead of composing the new intent. Keep the relay framing in
	// the system prompt too (reinforcement).
	msgs = append(msgs, Message{Role: "user", Content: fmt.Sprintf(
		"Compose a NEW outbound text to %s now, in your own voice, conveying this:\n\n%s\n\n"+
			"This is a fresh message. Do NOT repeat, resend, or echo any earlier message in this conversation. "+
			"Send ONLY the message itself — no narration, preamble, or announcing what you're about to do.",
		recipient, intent)})

	sysPrompt := fmt.Sprintf(
		"Current date and time: %s\n\nYour name is %s. The person you are messaging is %s.\n\n%s%s\n\n"+
			"## Relay Instruction\nYour operator wants you to convey the following to %s, in your own established voice and the context of this conversation. Phrase it naturally as a text FROM YOU — rework it into your voice rather than quoting verbatim, and do NOT mention an operator, that it was relayed, or that it's automated:\n\n%s\n\n"+
			"Send ONLY the message itself. Do NOT narrate or announce what you are about to do, and do NOT add preamble or process commentary (no \"Let me find...\", \"I'll send...\", \"One sec...\", \"Hold on...\"). If the intent is to share or do something, just say the accompanying message as you naturally would — never describe the act of doing it.\n\n"+
			"Compose a single natural outbound text message. Plain text only, no markdown.",
		time.Now().Format("Monday, January 2, 2006 3:04 PM MST"), personaName, recipient, basePrompt, memoryBlock(T.DB, chatID), recipient, intent,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := T.LLM.Chat(ctx, msgs,
		WithSystemPrompt(sysPrompt),
		WithMaxTokens(500),
		WithRouteKey("app.phantom"),
		WithThink(false),
	)
	if err != nil || resp == nil {
		Log("[phantom/operator-relay] compose failed for chat=%s: %v — sending intent verbatim", chatID, err)
		return deliver(intent)
	}
	if strings.TrimSpace(resp.Content) == "" {
		Log("[phantom/operator-relay] empty compose for chat=%s — sending intent verbatim", chatID)
		return deliver(intent)
	}
	Log("[phantom/operator-relay] composed in-voice reply for chat=%s (%d chars)", chatID, len(strings.TrimSpace(resp.Content)))
	return deliver(resp.Content)
}

// chatIDForHandle resolves an existing conversation for a handle (matching the
// primary handle or any alias) and returns its chat id; absent one, it falls
// back to the iMessage chat-id convention so a first outbound still addresses.
func (p phantomLinkImpl) chatIDForHandle(handle string) string {
	for _, k := range p.app.DB.Keys(conversationTable) {
		var c Conversation
		if !p.app.DB.Get(conversationTable, k, &c) {
			continue
		}
		if strings.EqualFold(c.Handle, handle) {
			return c.ChatID
		}
		for _, a := range c.AliasHandles {
			if strings.EqualFold(a, handle) {
				return c.ChatID
			}
		}
	}
	return "iMessage;-;" + handle
}
