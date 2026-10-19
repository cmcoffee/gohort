// The Operator's tools for reaching people on a channel: messaging a contact,
// authorizing a sender, and binding a thread to an agent. Listed in
// operatorToolDefsUnfolded (operator_tools.go).

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func messageContactToolDef(sess *ToolSession, owner, agentID, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "message_contact",
			Description: "Send an iMessage to a CONTACT or a GROUP (anyone other than the owner). Set `to` to the recipient as shown by list_chats: a contact/group NAME (e.g. \"WiWee\"), a handle (phone/email), or a chat_id. Any of them resolve to the right conversation, group chats included; you don't need to track the opaque chat_id: the name works. To send an image/file, pass its workspace path in `attachments`. Your exact words are sent verbatim. Contacting real people is consequential, so it queues for the user's approval (unless they pre-authorized that recipient via 'Always allow', or you're replying to someone who just messaged you), then sends once approved. When you need their answer, set read_reply=true: the same approval lets you read their reply.",
			Parameters: map[string]ToolParam{
				"to":          {Type: "string", Description: "Recipient as shown by list_chats: a contact/group name, a handle (phone/email), or a chat_id. Required: never omit it."},
				"text":        {Type: "string", Description: "The message to send. Do NOT type delivery markers like [ATTACH: ...] into this text; that is a different surface's convention and is stripped before sending."},
				"attachments": {Type: "array", Items: &ToolParam{Type: "string"}, Description: attachmentsParamDesc},
				"read_reply":  {Type: "boolean", Description: "Optional. true when you need their answer: approving the send also binds their 1:1 thread to you, so their reply wakes you (or poll it with await_result). One approval for both; no separate request_thread_binding."},
			},
			Required: []string{"to", "text"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			to := strings.TrimSpace(oArgStr(args, "to"))
			text := strings.TrimSpace(oArgStr(args, "text"))
			if err := missingArgs("the message_contact call",
				reqArg{"to", to, "the recipient as shown by list_chats: a contact/group name, a handle, or a chat_id"},
				reqArg{"text", text, "the message to send, sent verbatim"},
			); err != nil {
				return "", err
			}
			link, ok := ActiveMessagingLink()
			if !ok {
				return "", fmt.Errorf("the messaging bridge is not available")
			}
			rec, ok := link.ResolveRecipient(owner, to)
			if !ok {
				return "", fmt.Errorf("no conversation matches %q: set `to` to a contact/group name, handle, or chat_id exactly as shown by list_chats. If you have the person's NAME but not a number, resolve it with from_client_contacts_search first (it reads the local address book), then call message_contact with the number it returns.", to)
			}
			recip := operatorRecipientKey(rec.ChatID, rec.Handle)
			label := operatorRecipientLabel(rec)
			images, videos := messageMedia(sess, args, text)
			// read_reply: the agent needs their answer. Reading a person's
			// replies is never granted silently: a send that needs approval
			// carries the binding in that same approval, and one that goes
			// out without asking queues the binding as one approval of its
			// own. Already bound, or replying in-thread, needs nothing.
			readReply := argBool(args, "read_reply", false) && !agentBoundTo(owner, controllerAgentID, rec.ChatID, rec.Handle)
			bindAfterSend := func(sent string) string {
				if !readReply {
					return sent
				}
				return sent + " " + queueReplyBinding(owner, controllerAgentID, rec.ChatID, rec.Handle, label)
			}
			if familyContactBlocked(owner, agentID, recip) {
				return fmt.Sprintf("Messaging %s is blocked in the user's permission settings: not sent.", label), nil
			}
			// Replying to the conversation that just messaged us is in-thread,
			// not a proactive reach-out, so no approval queue; and it is the
			// turn's reply rather than a message beside it (replyInThread).
			if isReplyToActiveInbound(sess, recip) {
				return replyInThread(sess, label, text, images, videos), nil
			}
			// Pre-authorized recipient: send immediately, skip the queue,
			// unless someone else's request is reaching past where they wrote.
			standing := standingGrantApplies(ctx, recip)
			if standing && familyContactAllowed(owner, agentID, recip) {
				if _, err := operatorDeliverMedia(owner, agentID, rec.ChatID, rec.Handle, text, images, videos); err != nil {
					return "", err
				}
				// If the target is a bound channel, make its agent see the post
				// (channel session + cortex) so it can field follow-ups.
				recordChannelPost(sess.DB, owner, rec.ChatID, rec.Handle, text)
				return bindAfterSend(fmt.Sprintf("Sent to %s (you've pre-authorized this recipient).", label)), nil
			}
			// Authorized sender for this channel: a parent granted THIS agent the
			// right to deliver to its channel, OR this agent is a sub-agent of one
			// that may (inherited down the ownership chain). Send without queuing —
			// the grant is the approval.
			if standing && channelSenderAuthorized(UserDB(orchestrateBaseDB, owner), owner, rec.ChatID, rec.Handle, agentID) {
				if _, err := operatorDeliverMedia(owner, agentID, rec.ChatID, rec.Handle, text, images, videos); err != nil {
					return "", err
				}
				recordChannelPost(sess.DB, owner, rec.ChatID, rec.Handle, text)
				return bindAfterSend(fmt.Sprintf("Sent to %s (authorized sender for this channel).", label)), nil
			}
			// Don't queue a DUPLICATE. message_contact returns "queued for
			// approval" (not "sent"), which a model reads as "it didn't go
			// through" and re-sends a round later — stacking two identical
			// pending approvals that both fire on approval (the observed
			// double-text). If an identical send to this recipient is already
			// pending, point at it instead of re-queuing.
			for _, ex := range ListAuthorizations(RootDB, owner) {
				if ex.Action == "send_message" && ex.ChatID == rec.ChatID && ex.Handle == rec.Handle && ex.Text == text {
					return fmt.Sprintf("Already queued this exact message to %s for approval (id %s): it's awaiting the user, NOT re-sent. Don't queue it again; wait for the reply.", label, ex.ID), nil
				}
			}
			pending := Authorization{
				Owner: owner, Action: "send_message", ChatID: rec.ChatID, Handle: rec.Handle, Text: text, Images: images, Videos: videos,
			}
			if readReply {
				pending.BindReply, pending.FromAgent, pending.Brief = true, controllerAgentID, label
			}
			a := SaveAuthorization(RootDB, pending)
			if sess != nil && sess.PendingApprovalPrompt != nil {
				sess.PendingApprovalPrompt(a)
			}
			if readReply {
				return fmt.Sprintf("Queued a message to %s for the user's approval (id %s): once approved it sends, and their 1:1 thread is bound to you, so their reply wakes you (or poll it with await_result). One approval covers both: do not call request_thread_binding.", label, a.ID), nil
			}
			return fmt.Sprintf("Queued a message to %s for the user's approval: it's in the Authorizations pane (id %s) and sends once approved.", label, a.ID), nil
		},
	}
}

func authorizeChannelSenderToolDef(owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "authorize_channel_sender",
			Description: "Grant ANOTHER agent (e.g. a sub-agent you created) the right to deliver to one of your channels WITHOUT the per-send approval queue, so its scheduled/autonomous runs can post to that group. Names the channel (by name, bound address, or chat_id) and the agent (by name or id).",
			Parameters: map[string]ToolParam{
				"channel": {Type: "string", Description: "The channel to grant on: its name, its bound address/chat_id, or its id."},
				"agent":   {Type: "string", Description: "The agent to authorize: its name or id."},
			},
			Required: []string{"channel", "agent"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			chanRef := strings.TrimSpace(oArgStr(args, "channel"))
			agentRef := strings.TrimSpace(oArgStr(args, "agent"))
			if err := missingArgs("the authorize_channel_sender call",
				reqArg{"channel", chanRef, "the channel to grant on: its name, its bound address/chat_id, or its id"},
				reqArg{"agent", agentRef, "the agent to authorize, by name or id"},
			); err != nil {
				return "", err
			}
			target, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agentRef)
			if !ok {
				return "", fmt.Errorf("no agent matches %q", agentRef)
			}
			var ch Channel
			found := false
			for _, c := range ListChannels(RootDB, owner) {
				if c.Name == chanRef || c.Address == chanRef || c.ID == chanRef {
					ch, found = c, true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("no channel matches %q: use its name, bound address, or id", chanRef)
			}
			for _, s := range ch.AuthorizedSenders {
				if s == target.ID {
					return fmt.Sprintf("%s is already an authorized sender on channel %q.", target.Name, ch.Name), nil
				}
			}
			ch.AuthorizedSenders = append(ch.AuthorizedSenders, target.ID)
			SaveChannel(RootDB, ch)
			return fmt.Sprintf("Granted %s send access to channel %q: its autonomous runs can now post there without approval.", target.Name, ch.Name), nil
		},
	}
}

func requestThreadBindingToolDef(owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "request_thread_binding",
			Description: "Ask to bind a person's DIRECT 1:1 message thread so you can READ their replies. Use this when you message someone (message_contact) and need to see their answer: a 1:1 thread you aren't bound to is invisible to you. Give the person's handle/phone number (from the group's participant list via read_chat), NOT their display name. It queues for the user's approval; once approved the thread is a persistent channel bound to you, gatekept so you only engage on replies to your own messages. Set wake=false to bind it read-only (no auto-wake, gatekeeper off) and poll it yourself with await_result instead. About to message them? Pass read_reply=true to message_contact instead: one approval covers the send and the binding.",
			Parameters: map[string]ToolParam{
				"to":   {Type: "string", Description: "The person's handle/phone number (or a chat_id) for their 1:1 thread. A display name will not resolve; use the number from read_chat's participant list."},
				"wake": {Type: "boolean", Description: "Optional, default true. true = the thread wakes you on replies (gatekept to your own conversation). false = read-only, no auto-wake; poll it with await_result."},
			},
			Required: []string{"to"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			to := strings.TrimSpace(oArgStr(args, "to"))
			if to == "" {
				return "", fmt.Errorf("to is required, the person's handle/number for their 1:1 thread")
			}
			link, ok := ActiveMessagingLink()
			if !ok {
				return "", fmt.Errorf("the messaging bridge is not available")
			}
			rec, ok := link.ResolveRecipient(owner, to)
			if !ok {
				return "", fmt.Errorf("couldn't resolve %q to a 1:1 thread, pass the person's phone number/handle (from read_chat's participant list), not their display name", to)
			}
			label := operatorRecipientLabel(rec)
			if agentBoundTo(owner, controllerAgentID, rec.ChatID, rec.Handle) {
				return fmt.Sprintf("You're already bound to %s's thread.", label), nil
			}
			if id, pending := replyBindingPending(owner, controllerAgentID, rec.ChatID, rec.Handle); pending {
				return fmt.Sprintf("A binding request for %s is already awaiting the user's approval (id %s).", label, id), nil
			}
			wakePref := ""
			if !argBool(args, "wake", true) {
				wakePref = "nowake" // carried in Text; the approval reads it
			}
			a := SaveAuthorization(RootDB, Authorization{
				Owner: owner, Action: "bind_thread", Agent: controllerAgentID,
				ChatID: rec.ChatID, Handle: rec.Handle, Brief: label, Text: wakePref,
			})
			return fmt.Sprintf("Requested a binding to %s's 1:1 thread, queued for the user's approval (id %s). Once approved you can read their replies.", label, a.ID), nil
		},
	}
}

func setThreadWakeToolDef(owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "set_thread_wake",
			Description: "Turn wake on or off for a 1:1 thread you bound with request_thread_binding. wake=true means replies wake you (gatekept to your own conversation); wake=false makes it read-only (no auto-wake, gatekeeper off) so you poll it yourself with await_result. Only affects your own bound threads; no approval needed.",
			Parameters: map[string]ToolParam{
				"to":   {Type: "string", Description: "The bound person's handle/number or chat_id."},
				"wake": {Type: "boolean", Description: "true = wake on replies; false = read-only / no-wake."},
			},
			Required: []string{"to", "wake"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			to := strings.TrimSpace(oArgStr(args, "to"))
			if to == "" {
				return "", fmt.Errorf("to is required")
			}
			wake := argBool(args, "wake", true)
			ch, ok := findAgentBoundChannel(owner, controllerAgentID, to)
			if !ok {
				return "", fmt.Errorf("you have no bound thread matching %q: request one with request_thread_binding first", to)
			}
			ch.AutoReply = wake
			if wake {
				ch.Gatekeeper = threadBindingGatekeeperRule
			} else {
				ch.Gatekeeper = ""
			}
			SaveChannel(RootDB, ch)
			if wake {
				return fmt.Sprintf("%s will now wake you on replies.", ch.Name), nil
			}
			return fmt.Sprintf("%s is now read-only (no wake): poll it with await_result.", ch.Name), nil
		},
	}
}

func releaseThreadBindingToolDef(owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "release_thread_binding",
			Description: "Release a 1:1 thread binding you no longer need (e.g. once you have the answer you were waiting for). This only drops YOUR read access to that person's thread; it affects nobody else. No approval needed, and it only works on threads you bound yourself.",
			Parameters: map[string]ToolParam{
				"to": {Type: "string", Description: "The bound person's handle/number or chat_id."},
			},
			Required: []string{"to"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			to := strings.TrimSpace(oArgStr(args, "to"))
			if to == "" {
				return "", fmt.Errorf("to is required")
			}
			ch, ok := findAgentBoundChannel(owner, controllerAgentID, to)
			if !ok {
				return "", fmt.Errorf("you have no bound thread matching %q", to)
			}
			DeleteChannel(RootDB, owner, ch.ID)
			return fmt.Sprintf("Released the binding to %s's thread.", ch.Name), nil
		},
	}
}
