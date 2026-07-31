package bridges

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Self-thread loop protection.
//
// When the owner messages THEMSELVES, every message in that thread — theirs and
// the agent's alike — is is_from_me, and the daemon clears the handle for those.
// So an agent reply, delivered into the owner's own thread, comes back as an
// inbound that is indistinguishable by handle from the owner having typed it.
// It carries a fresh message id, so the id dedupe below doesn't catch it, and
// the channel it routes to is the same one that produced it. The agent answers
// its own answer, and each answer produces the next one.
//
// A duplicate delivery is what usually starts it: the same inbound handled
// twice produces two replies, which arrive as two more inbounds, and the branch
// widens instead of settling.
//
// Four guards, ordered by how conclusive they are:
//
//  1. carriesOurTag — the outbound name tag ("[Gohort] ") is a marker WE put on
//     the wire, so anything wearing it is our own message returning. Survives
//     rephrasing and transport changes alike, and needs no timing assumption.
//     This is the one that catches the observed loop.
//  2. isOwnEcho — a content fingerprint, for outbound that carried no tag.
//     Exact, and useless the moment the agent rewords its reply.
//  3. seenContent — dedupe for deliveries arriving with NO message id, which the
//     id-keyed dedupe treats as new every time. This is the duplicate delivery
//     that starts a loop.
//  4. noteReply — a cap on replies per conversation. Catches any shape the
//     others miss. Strict in a self thread (the only place this can happen) and
//     generous elsewhere, so it never becomes the bug it prevents.
//
// Everything is keyed on the PERSON, not the chat id: iMessage falls back to
// SMS/MMS, and those are separate ids for one thread — a reply sent one way and
// reflected the other defeated all of it until the keys collapsed onto the
// handle.
//
// State is in-memory and TTL'd: the windows are minutes, and a process restart
// starting a loop is not a shape that occurs.

const (
	// echoTTL bounds how long a sent message stays recognizable as ours. Long
	// enough to cover slow round-trips through the daemon, short enough that a
	// user deliberately re-sending the same text later isn't swallowed.
	echoTTL = 10 * time.Minute
	// contentTTL dedupes id-less re-deliveries of identical text.
	contentTTL = 2 * time.Minute
	// replyWindow / replyBudget: at most this many agent replies into one
	// conversation per window before routing is cut. Sized so an ordinary rapid
	// exchange never reaches it — a human sending eight messages inside two
	// minutes and expecting eight separate answers is not a real pattern, while
	// a loop clears it in seconds.
	// A loop runs at the agent's speed, not a human's: at ~13s per round eight
	// replies take just over two minutes, so a two-minute window never filled
	// and the budget never tripped on the very case it was written for. The
	// window has to outlast a slow loop, and the count has to stay above a real
	// exchange — ten minutes and twelve replies clears both.
	replyWindowDefault = 10 * time.Minute
	replyBudgetDefault = 12
	// A loop of this shape is only possible in a thread addressed to YOURSELF:
	// anywhere else the other end is a person who has to actually type, so the
	// agent can never be answering its own words. That means the self thread can
	// be policed hard and every other conversation left alone — the budget above
	// is a backstop for real threads, this is the real limit for the one place a
	// runaway can start. Three consecutive agent messages into your own thread
	// is already unusual; thirty is a loop.
	selfThreadBudgetDefault = 3
	// loopCooldown is how long routing stays cut for a conversation once the
	// budget trips. Inbound is still recorded; nothing wakes the agent.
	loopCooldownDefault = 10 * time.Minute
)

// The four numbers above are TUNABLE, because the right value depends on how
// the deployment is actually used and the wrong one is invisible until it
// bites. Twelve replies in ten minutes reads as generous until someone is
// iterating on an agent from their phone — every guardrail decline is a reply,
// so a testing session burns the budget and then loses ten minutes of routing
// to a loop that was never happening. Defaults unchanged; the knob exists so
// that costs a setting rather than a rebuild.
func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_bridge_reply_budget", Category: "Limits",
		Label:   "Loop guard: agent replies per conversation",
		Help:    "How many replies the agent may send into ONE conversation inside the window below before routing is cut as a suspected loop. Every reply counts, including a guardrail decline — so testing an agent from your phone spends this budget. Raise it if ordinary use trips the cut; lower it to catch runaways sooner.",
		Kind:    KindInt,
		Default: replyBudgetDefault, Min: 2, Max: 200,
	})
	RegisterTunable(TunableSpec{
		Key: "tune_bridge_reply_window_min", Category: "Limits",
		Label:   "Loop guard: reply window (minutes)",
		Help:    "The window the reply budget is counted over. It has to outlast a slow loop (an agent loop runs at ~13s per round) while staying short enough that a normal conversation's replies age out.",
		Kind:    KindInt,
		Default: float64(replyWindowDefault / time.Minute), Min: 1, Max: 120,
	})
	RegisterTunable(TunableSpec{
		Key: "tune_bridge_self_thread_budget", Category: "Limits",
		Label:   "Loop guard: replies into your own thread",
		Help:    "The stricter budget for a thread addressed to YOURSELF, where an agent can end up answering its own messages. Anywhere else a person has to type, so a runaway cannot start. Note this only applies when the transport keys the conversation by your handle; a conversation keyed by chat id falls under the general budget above.",
		Kind:    KindInt,
		Default: selfThreadBudgetDefault, Min: 1, Max: 50,
	})
	RegisterTunable(TunableSpec{
		Key: "tune_bridge_loop_cooldown_min", Category: "Limits",
		Label:   "Loop guard: cooldown (minutes)",
		Help:    "How long routing stays cut for a conversation after the budget trips. Inbound messages are still recorded to the transcript throughout; nothing wakes the agent until it expires.",
		Kind:    KindInt,
		Default: float64(loopCooldownDefault / time.Minute), Min: 1, Max: 240,
	})
}

func replyBudgetFor() int {
	if n := TuneInt("tune_bridge_reply_budget"); n > 0 {
		return n
	}
	return replyBudgetDefault
}

func selfThreadBudgetFor() int {
	if n := TuneInt("tune_bridge_self_thread_budget"); n > 0 {
		return n
	}
	return selfThreadBudgetDefault
}

func replyWindowFor() time.Duration {
	if n := TuneInt("tune_bridge_reply_window_min"); n > 0 {
		return time.Duration(n) * time.Minute
	}
	return replyWindowDefault
}

func loopCooldownFor() time.Duration {
	if n := TuneInt("tune_bridge_loop_cooldown_min"); n > 0 {
		return time.Duration(n) * time.Minute
	}
	return loopCooldownDefault
}

var loopGuard struct {
	mu sync.Mutex
	// echo: fingerprint → when we sent it.
	echo map[string]time.Time
	// content: identity+text hash → when we last saw it inbound.
	content map[string]time.Time
	// replies: identity → recent agent-reply timestamps.
	replies map[string][]time.Time
	// tripped: identity → when the budget blew, for the cooldown.
	tripped map[string]time.Time
	// tags: outbound name-tag prefixes we have emitted ("[gohort] ").
	tags map[string]bool
}

func loopGuardInit() {
	if loopGuard.echo == nil {
		loopGuard.echo = map[string]time.Time{}
		loopGuard.content = map[string]time.Time{}
		loopGuard.replies = map[string][]time.Time{}
		loopGuard.tripped = map[string]time.Time{}
		loopGuard.tags = map[string]bool{}
	}
}

// noteOutboundTag remembers a name-tag prefix we put on the wire ("[Gohort] ").
// The tag exists so a recipient can tell an agent's message from the owner's
// own texts — which makes it, for free, the most reliable mark of OUR message
// coming back. Unlike the content fingerprint it survives rephrasing, and
// unlike the reply budget it needs no timing assumption.
func noteOutboundTag(prefix string) {
	if prefix = strings.TrimSpace(prefix); prefix == "" {
		return
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	loopGuard.tags[strings.ToLower(prefix)] = true
}

// carriesOurTag reports whether an inbound opens with a tag we emit. An agent
// message that comes back into the thread that produced it is never something
// to answer, however it was worded and whichever transport carried it.
func carriesOurTag(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || !strings.HasPrefix(t, "[") {
		return false
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	for tag := range loopGuard.tags {
		if strings.HasPrefix(t, tag) {
			return true
		}
	}
	return false
}

// loopIdentity reduces a conversation to the PERSON it reaches, not the
// transport it took. iMessage delivers as native iMessage or falls back to
// SMS/MMS, and those are different chat ids for the same thread — so a reply
// that goes out one way and reflects back the other looked like two unrelated
// conversations. That defeated every guard here at once: the fingerprint never
// matched because it was scoped to the id, and the reply budget split across
// two buckets instead of filling one. chatHandle already strips the service
// prefix ("iMessage;-;+1650…" and "SMS;-;+1650…" both reduce to "+1650…"), so
// both legs collapse onto one key.
//
// Groups keep their own chat id — a group is identified by the group, never by
// whichever member happened to speak, matching the rule inboundIdentities
// already follows. chatHandle returns "" for a group, so an empty result there
// is the group signal, and the chat id must be preferred over the sender's
// handle on that branch.
//
// Getting that order backwards broke group threads outright: every guard here
// keys on this value, so a group borrowed the identity of whoever spoke. One
// person's 1:1 thread and every group they were in shared a single reply budget
// and a single cooldown, so a self-thread loop — the exact incident this guard
// exists for — tripped once and then cut routing for unrelated group
// conversations. Worse, the owner's own message in a group arrives over SMS/MMS
// with their handle populated, which made loopIdentity equal SelfHandle and
// isSelfThread report TRUE for a group, dropping it to the strict 3-reply
// budget. The echo fingerprint mismatched too: outbound to a group carries no
// handle and keyed on the chat id, while inbound keyed on the member.
func loopIdentity(chatID, handle string) string {
	if h := chatHandle(chatID); h != "" {
		return normalizeIdentity(h) // 1:1 — collapse the iMessage and SMS legs onto the person
	}
	if id := strings.TrimSpace(chatID); id != "" {
		return normalizeIdentity(id) // group — the conversation itself is the identity
	}
	return normalizeIdentity(handle)
}

// normalizeIdentity flattens the cosmetic differences between renderings of the
// same handle — "+1 (650) 555-1234" and "+16505551234" are one person, and an
// address differing only in case is one mailbox.
func normalizeIdentity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '-', '(', ')', '.', '\t':
			// drop punctuation that only ever varies by formatting
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// echoKey fingerprints a message by WHO it reaches + its text. Whitespace is
// normalized because transports reflow it; the text is otherwise compared as
// sent, AFTER the outbound tag and markdown flattening, since that is the form
// that comes back.
func echoKey(chatID, handle, text string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(loopIdentity(chatID, handle) + "\x00" + norm))
	return hex.EncodeToString(sum[:16])
}

// isSelfThread reports whether a conversation is addressed to the owner
// THEMSELVES. That is the only place this loop can occur: every other thread
// has a person on the other end who has to type, so the agent is never
// answering its own words. Knowing which is which lets the strict limit apply
// where a runaway is possible and leave real conversations alone.
func (T *Bridges) isSelfThread(chatID, handle string) bool {
	self := strings.TrimSpace(T.config().SelfHandle)
	if self == "" {
		return false
	}
	return loopIdentity(chatID, handle) == normalizeIdentity(self)
}

// isOwnerHandle reports whether an inbound came from the OWNER's own handle.
//
// "From me" cannot be read off an empty handle alone. The daemon clears the
// handle for a native is_from_me iMessage, but the same thread delivered as
// SMS/MMS arrives as a RECEIVED message from the owner's own number, handle
// populated — so the empty-handle test skipped the echo guard on exactly the
// self-thread it was written for. Comparing against the configured SelfHandle
// covers both legs.
func (T *Bridges) isOwnerHandle(handle string) bool {
	if strings.TrimSpace(handle) == "" {
		return true // is_from_me: the daemon clears the handle
	}
	self := strings.TrimSpace(T.config().SelfHandle)
	if self == "" {
		return false
	}
	return normalizeIdentity(handle) == normalizeIdentity(self)
}

// noteOutbound records that we sent this text into this conversation, so the
// copy that comes back is recognizable as ours. Called from the single outbound
// chokepoint (enqueueOutbox) with the FINAL text.
func noteOutbound(chatID, handle, text string) {
	k := echoKey(chatID, handle, text)
	if k == "" {
		return
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	sweepLocked()
	loopGuard.echo[k] = time.Now()
}

// isOwnEcho reports whether an inbound is a message we sent coming back. Only
// meaningful for from-me messages: another person quoting our exact words is
// theirs to answer, ours is not.
func isOwnEcho(chatID, handle, text string, fromMe bool) bool {
	if !fromMe {
		return false
	}
	k := echoKey(chatID, handle, text)
	if k == "" {
		return false
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	at, ok := loopGuard.echo[k]
	if !ok || time.Since(at) > echoTTL {
		return false
	}
	// Consume it: a genuine second send of the same text should be answered.
	delete(loopGuard.echo, k)
	return true
}

// seenContent dedupes a delivery that arrived with no message id, where the
// id-keyed dedupe has nothing to key on and treats every copy as new.
func seenContent(chatID, handle, text string) bool {
	k := echoKey(chatID, handle, text)
	if k == "" {
		return false
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	sweepLocked()
	if at, ok := loopGuard.content[k]; ok && time.Since(at) < contentTTL {
		return true
	}
	loopGuard.content[k] = time.Now()
	return false
}

// noteReply records that the agent answered into this conversation and reports
// whether that has now blown the budget. The caller stops routing on true.
// selfThread selects the strict limit — see selfThreadBudget.
func noteReply(chatID, handle string, selfThread bool) (tripped bool) {
	id := loopIdentity(chatID, handle)
	if id == "" {
		return false
	}
	budget := replyBudgetFor()
	if selfThread {
		budget = selfThreadBudgetFor()
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	sweepLocked()
	now := time.Now()
	kept := loopGuard.replies[id][:0]
	for _, at := range loopGuard.replies[id] {
		if now.Sub(at) < replyWindowFor() {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	loopGuard.replies[id] = kept
	if len(kept) >= budget {
		loopGuard.tripped[id] = now
		return true
	}
	return false
}

// loopTripped reports whether a conversation is in its post-loop cooldown.
func loopTripped(chatID, handle string) bool {
	id := loopIdentity(chatID, handle)
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	at, ok := loopGuard.tripped[id]
	if !ok {
		return false
	}
	if time.Since(at) > loopCooldownFor() {
		delete(loopGuard.tripped, id)
		delete(loopGuard.replies, id)
		return false
	}
	return true
}

// sweepLocked drops expired entries so the maps track live conversations rather
// than growing for the process's lifetime. Caller holds the lock.
func sweepLocked() {
	now := time.Now()
	for k, at := range loopGuard.echo {
		if now.Sub(at) > echoTTL {
			delete(loopGuard.echo, k)
		}
	}
	for k, at := range loopGuard.content {
		if now.Sub(at) > contentTTL {
			delete(loopGuard.content, k)
		}
	}
	for k, at := range loopGuard.tripped {
		if now.Sub(at) > loopCooldownFor() {
			delete(loopGuard.tripped, k)
			delete(loopGuard.replies, k)
		}
	}
}

// LoopGuardReset clears all state. Test seam.
func LoopGuardReset() {
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuard.echo = nil
	loopGuardInit()
}

// logLoopCut reports a tripped conversation once, loudly. A loop that is
// silently contained still means the agent burned a run per round and the owner
// saw a burst of texts — they need to know which thread did it.
func logLoopCut(chatID string) {
	Log("[bridges] LOOP GUARD: conversation %s produced %d agent replies in %s — routing CUT for %s. "+
		"Inbound is still recorded; nothing wakes the agent. Common cause: a self-thread where the agent's own "+
		"replies arrive back as owner messages.", chatID, replyBudgetFor(), replyWindowFor(), loopCooldownFor())
}
