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
// Three independent guards, because each fails differently:
//
//  1. echoGuard — the outbound chokepoint fingerprints what we send; an inbound
//     from-me message matching a recent fingerprint is our own words coming
//     back. Exact, and useless the moment the agent rephrases.
//  2. contentSeen — dedupe for deliveries that arrive with NO message id, which
//     the id-keyed dedupe treats as new every time.
//  3. replyBudget — a hard cap on replies per conversation per window. Catches
//     any loop shape the first two miss, including an agent that says something
//     different every round. This is the one that actually guarantees
//     termination; the others just keep it from tripping in normal use.
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
	replyWindow = 2 * time.Minute
	replyBudget = 8
	// loopCooldown is how long routing stays cut for a conversation once the
	// budget trips. Inbound is still recorded; nothing wakes the agent.
	loopCooldown = 10 * time.Minute
)

var loopGuard struct {
	mu sync.Mutex
	// echo: fingerprint → when we sent it.
	echo map[string]time.Time
	// content: chat+text hash → when we last saw it inbound.
	content map[string]time.Time
	// replies: chat id → recent agent-reply timestamps.
	replies map[string][]time.Time
	// tripped: chat id → when the budget blew, for the cooldown.
	tripped map[string]time.Time
}

func loopGuardInit() {
	if loopGuard.echo == nil {
		loopGuard.echo = map[string]time.Time{}
		loopGuard.content = map[string]time.Time{}
		loopGuard.replies = map[string][]time.Time{}
		loopGuard.tripped = map[string]time.Time{}
	}
}

// echoKey fingerprints a message by conversation + its text. Whitespace is
// normalized because transports reflow it; the text is otherwise compared as
// sent, AFTER the outbound tag and markdown flattening, since that is the form
// that comes back.
func echoKey(chatID, text string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(chatID + "\x00" + norm))
	return hex.EncodeToString(sum[:16])
}

// noteOutbound records that we sent this text into this conversation, so the
// copy that comes back is recognizable as ours. Called from the single outbound
// chokepoint (enqueueOutbox) with the FINAL text.
func noteOutbound(chatID, text string) {
	k := echoKey(chatID, text)
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
func isOwnEcho(chatID, text string, fromMe bool) bool {
	if !fromMe {
		return false
	}
	k := echoKey(chatID, text)
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
func seenContent(chatID, text string) bool {
	k := echoKey(chatID, text)
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
func noteReply(chatID string) (tripped bool) {
	if strings.TrimSpace(chatID) == "" {
		return false
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	sweepLocked()
	now := time.Now()
	kept := loopGuard.replies[chatID][:0]
	for _, at := range loopGuard.replies[chatID] {
		if now.Sub(at) < replyWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	loopGuard.replies[chatID] = kept
	if len(kept) >= replyBudget {
		loopGuard.tripped[chatID] = now
		return true
	}
	return false
}

// loopTripped reports whether a conversation is in its post-loop cooldown.
func loopTripped(chatID string) bool {
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuardInit()
	at, ok := loopGuard.tripped[chatID]
	if !ok {
		return false
	}
	if time.Since(at) > loopCooldown {
		delete(loopGuard.tripped, chatID)
		delete(loopGuard.replies, chatID)
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
		if now.Sub(at) > loopCooldown {
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
		"replies arrive back as owner messages.", chatID, replyBudget, replyWindow, loopCooldown)
}
