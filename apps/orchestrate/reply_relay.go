// Passing a person's answer back to the conversation that asked them.
//
// An agent asked to find something out from someone messages them and says
// it will report what they say. Observed: "Sent it, asked him directly...
// Once he answers, I'll tell you what he says." Nothing then happened when he
// answered. His thread was bound to a channel of its own, so his reply woke
// that channel's agent in his conversation, which answered him and knew
// nothing of the owner waiting; and a thread with no channel, or one that
// only records, would not have woken anything at all.
//
// So a send from a conversation arms a one-shot relay: the next message that
// person sends wakes the conversation that asked, with what they said, the way
// a background job's result does. Whatever their own thread does with the
// message carries on unchanged. It is armed when the message actually goes
// out (at once, or when the owner approves it), never for a send that was
// refused, and it expires after replyRelayTTL.
//
// It waits on a PERSON, by their handle, and only where the answer would not
// otherwise reach the agent. Observed: an agent's cortex posted into a group
// chat the same agent serves, and thirteen hours later the next message in
// that group, a request to the agent from somebody else, both reached the
// group's own conversation, which answered it, and woke the cortex as "an
// answer", which acted on it too: two pictures rendered for one request, the
// cortex's left in the cortex with nobody to send it to. So a send to a group
// arms nothing (a group has no single person whose next message is the
// answer), a send into a chat the asking agent serves arms nothing (the
// answer reaches that agent there, with what was said), and a relay keeps for
// hours, not a day.

package orchestrate

import (
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/messaging"
)

// replyRelayTable holds each owner's armed relays, keyed by owner.
const replyRelayTable = "orchestrate_reply_relays"

// replyRelayTTL is how long a relay waits for the answer. Long enough for
// somebody to see it and reply; past it, the conversation has moved on, and
// their next message is likelier to be about something else than to be the
// answer promised. It was a day, and a post at 1:38am was answered by a
// different request at 3:06pm.
const replyRelayTTL = 4 * time.Hour

// replyWakeMarker opens the note a relayed answer wakes a conversation with.
// A wake like a finished task's (isTaskWake).
const replyWakeMarker = "[REPLY ARRIVED]"

// replyRelay is one conversation waiting on one person's next message.
type replyRelay struct {
	ChatID string     `json:"chat_id,omitempty"` // the conversation the message went to
	Handle string     `json:"handle,omitempty"`
	Sent   string     `json:"sent"` // what was sent them, so the wake can say what this answers
	Origin taskOrigin `json:"origin"`
	Until  time.Time  `json:"until"`
}

var replyRelayMu sync.Mutex

// askingOrigin is the conversation a live turn would be woken in.
func askingOrigin(sess *ToolSession) taskOrigin {
	if sess == nil {
		return taskOrigin{}
	}
	agentID, _ := liveRunRegistry().threadAgent(sess)
	return taskOrigin{
		SessionID: sess.DeliverySession(), User: sess.Username, AgentID: agentID,
		ChatID: strings.TrimSpace(sess.ChannelChatID), Handle: strings.TrimSpace(sess.ChannelHandle),
		Spoken: sess.SpokenReply,
	}
}

// armReplyRelay waits for the next message from the person a message just went
// to, on behalf of the conversation in origin. A conversation already waiting
// on them is re-armed with the newer message. Nothing is armed without a
// conversation to come back to, when the asking conversation IS theirs, without
// a person to wait on (a group: its chat has no handle), or into a chat the
// asking agent serves itself, where their answer already reaches it.
func armReplyRelay(owner, chatID, handle, sent string, origin taskOrigin) {
	chatID, handle = strings.TrimSpace(chatID), strings.TrimSpace(handle)
	if owner == "" || origin.SessionID == "" || origin.AgentID == "" || handle == "" {
		return
	}
	if sameRecipient(chatID, handle, origin.ChatID, origin.Handle) {
		return
	}
	if agentServesChat(owner, origin.AgentID, chatID, handle) {
		return
	}
	replyRelayMu.Lock()
	defer replyRelayMu.Unlock()
	now := time.Now()
	var kept []replyRelay
	for _, r := range loadReplyRelays(owner) {
		if now.After(r.Until) || (r.Origin.SessionID == origin.SessionID && sameRecipient(r.ChatID, r.Handle, chatID, handle)) {
			continue
		}
		kept = append(kept, r)
	}
	kept = append(kept, replyRelay{ChatID: chatID, Handle: handle, Sent: strings.TrimSpace(sent), Origin: origin, Until: now.Add(replyRelayTTL)})
	saveReplyRelays(owner, kept)
	Log("[reply-relay] %s: session %s waits on the next message from %s", owner, origin.SessionID, operatorRecipientKey(chatID, handle))
}

// takeReplyRelays removes and returns the relays an inbound answers, dropping
// any that expired on the way. Our own message coming back as an inbound copy
// answers nothing.
func takeReplyRelays(ev messaging.InboundEvent) []replyRelay {
	addrs := map[string]bool{}
	for _, a := range append([]string{ev.ChatID, ev.Handle}, ev.Addresses...) {
		if a = strings.TrimSpace(a); a != "" {
			addrs[a] = true
		}
	}
	replyRelayMu.Lock()
	defer replyRelayMu.Unlock()
	all := loadReplyRelays(ev.Owner)
	if len(all) == 0 {
		return nil
	}
	now := time.Now()
	text := strings.TrimSpace(ev.Text)
	var due, kept []replyRelay
	for _, r := range all {
		switch {
		case now.After(r.Until):
		// The person, by their handle (the inbound's addresses carry the
		// sender's handle and aliases). Not the chat: in a group the chat is
		// everybody's, and a relay armed before this rule on a chat alone
		// waits out its time and goes.
		case r.Handle != "" && addrs[r.Handle] && text != r.Sent:
			due = append(due, r)
		default:
			kept = append(kept, r)
		}
	}
	if len(kept) != len(all) {
		saveReplyRelays(ev.Owner, kept)
	}
	return due
}

// relayReply is the inbound watcher: every conversation waiting on this
// person is woken with what they said.
func (T *OrchestrateApp) relayReply(ev messaging.InboundEvent) {
	if strings.TrimSpace(ev.Owner) == "" {
		return
	}
	for _, r := range takeReplyRelays(ev) {
		r := r
		note := replyWakeNote(ev, r)
		Log("[reply-relay] %s: %s answered, waking session %s", ev.Owner, chFirst(ev.Sender, ev.Handle), r.Origin.SessionID)
		go func() {
			if err := T.wakeSessionWithNote(r.Origin, note); err != nil {
				Warn("[reply-relay] could not wake session %s with the answer: %v", r.Origin.SessionID, err)
			}
		}()
	}
}

// replyWakeNote is the answer as the waiting conversation receives it: what
// they said and what it answers kept in the thread, the instruction for this
// turn only.
func replyWakeNote(ev messaging.InboundEvent, r replyRelay) wakeNote {
	who := chFirst(strings.TrimSpace(ev.Sender), strings.TrimSpace(ev.Handle), "They")
	var fact strings.Builder
	fact.WriteString(replyWakeMarker + " " + who + " answered the message you sent them")
	if q := wakeRequestQuote(r.Sent); q != "" {
		fact.WriteString(", " + q)
	}
	answer := strings.TrimSpace(ev.Text)
	if a := []rune(answer); len(a) > 2000 {
		answer = string(a[:2000]) + "…"
	}
	fact.WriteString(":\n\"" + answer + "\"\n")
	act := "Tell the user what they said now, as the answer you said you would pass on. Their own conversation with them goes on separately: do not message them again unless the user asks you to."
	return wakeNote{prompt: fact.String() + act, history: strings.TrimSpace(fact.String())}
}

// sameRecipient reports whether two chat/handle pairs name one conversation.
// agentServesChat reports whether agentID has a channel of its own on this
// chat (bound to the chat id or the person's handle) that takes inbound: their
// messages there already reach the agent, in that channel's conversation.
func agentServesChat(owner, agentID, chatID, handle string) bool {
	for _, ch := range ListChannels(RootDB, owner) {
		if ch.AgentID != agentID || ch.Address == "" || ch.Direction == messaging.DirectionOutbound {
			continue
		}
		if (chatID != "" && ch.Address == chatID) || ch.Address == handle {
			return true
		}
	}
	return false
}

func sameRecipient(chatA, handleA, chatB, handleB string) bool {
	return (chatA != "" && (chatA == chatB || chatA == handleB)) || (handleA != "" && (handleA == handleB || handleA == chatB))
}

func loadReplyRelays(owner string) []replyRelay {
	var out []replyRelay
	if RootDB != nil {
		RootDB.Get(replyRelayTable, owner, &out)
	}
	return out
}

func saveReplyRelays(owner string, rs []replyRelay) {
	if RootDB == nil {
		return
	}
	if len(rs) == 0 {
		RootDB.Unset(replyRelayTable, owner)
		return
	}
	RootDB.Set(replyRelayTable, owner, rs)
}
