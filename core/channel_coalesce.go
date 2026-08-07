// Channel message coalescing.
//
// Messaging surfaces (iMessage and the like) routinely split one thought across
// rapid bubbles: a question, then an image posted right after. The transport
// spawns one dispatch per inbound, so without coalescing those two bubbles race
// as separate agent turns on the SAME session — each loads history before the
// other has written back, so the question turn never sees the image and the
// image turn never sees the question, and the last writer clobbers the other's
// exchange out of the transcript. That is the "Wiwee replied to a bare image as
// if it were a meme" failure.
//
// The coalescer holds each inbound for a short window (a tunable; 0 disables it)
// and merges any same-session follow-ups that land during it into ONE turn. A
// message that arrives AFTER a turn has already started, but before it has
// replied, cancels and reprocesses that turn with both messages folded in —
// the same "fresh send cancels old" backbone the web chat uses via RunRegistry.
// Once a turn has actually replied there is nothing left to cancel (you can't
// unsend a text), which is exactly why the front-hold window exists: it stops a
// fast single-call reply from shipping mid-thought in the first place.
//
// This is a transport concern, so it wraps the dispatch at the messaging
// transport only (see apps/bridges) — the web-chat and MCP request/response
// paths, where a per-call session would just eat the window as dead latency,
// call the agent directly and never touch this.

package core

import (
	"context"
	"strings"
	"sync"
	"time"
)

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_channel_coalesce_ms",
		Category: "Limits",
		Label:    "Channel message coalesce window (ms)",
		Help:     "How long an inbound channel message is held so rapid follow-ups from the same conversation (a question, then an image posted right after) merge into ONE agent turn instead of racing as separate turns on a shared session. A message that arrives while a turn is still running cancels and reprocesses it with both messages folded in, unless that turn already replied. 0 disables coalescing (each message dispatches immediately).",
		Kind:     KindInt,
		Default:  1500,
		Min:      0,
		Max:      10000,
	})
}

// channelCoalesceWindow reads the current hold window. <= 0 means disabled.
func channelCoalesceWindow() time.Duration {
	return time.Duration(TuneInt("tune_channel_coalesce_ms")) * time.Millisecond
}

// channelDispatchTimeout bounds a single coalesced dispatch, mirroring the
// generous per-goroutine bound the transport used before coalescing owned the
// context (agent turns with tool work legitimately run tens of seconds).
const channelDispatchTimeout = 10 * time.Minute

const (
	phaseCollecting = iota // holding the batch open for more messages
	phaseRunning           // the merged batch is dispatched and executing
)

// coalesceState is the per-session batch. gen identifies the current dispatch
// so a superseded (cancelled) run can tell it lost ownership and must discard
// its result instead of replying or clearing state.
type coalesceState struct {
	pending  ChannelInbound
	deadline time.Time
	phase    int
	gen      int
	cancel   context.CancelFunc
}

// ChannelCoalescer batches rapid inbound messages per session. Safe for
// concurrent use; one instance backs all messaging-transport dispatches.
type ChannelCoalescer struct {
	mu   sync.Mutex
	sess map[string]*coalesceState
}

// NewChannelCoalescer returns an empty coalescer.
func NewChannelCoalescer() *ChannelCoalescer {
	return &ChannelCoalescer{sess: map[string]*coalesceState{}}
}

var defaultChannelCoalescer = NewChannelCoalescer()

// CoalesceChannelDispatch runs `in` through the process-wide coalescer keyed by
// `key` (the session id). Exactly one of a merged batch's callers returns the
// real reply; the others return an empty ChannelReply (the transport sends
// nothing for those). `run` is the actual dispatch (RunChannelAgent).
func CoalesceChannelDispatch(key string, in ChannelInbound, run ChannelAgentRunnerFunc) (ChannelReply, error) {
	return defaultChannelCoalescer.Dispatch(key, in, run)
}

// Dispatch is the coalescing entry point. See CoalesceChannelDispatch.
func (c *ChannelCoalescer) Dispatch(key string, in ChannelInbound, run ChannelAgentRunnerFunc) (ChannelReply, error) {
	window := channelCoalesceWindow()
	if window <= 0 || key == "" {
		// Coalescing off, or no session to key on: run immediately, unbatched.
		ctx, cancel := context.WithTimeout(context.Background(), channelDispatchTimeout)
		defer cancel()
		return run(ctx, in)
	}

	c.mu.Lock()
	st := c.sess[key]
	switch {
	case st == nil:
		// First message for this session: open a fresh batch and lead it.
		c.sess[key] = &coalesceState{pending: in, deadline: time.Now().Add(window), phase: phaseCollecting}
		c.mu.Unlock()
		return c.lead(key, run, window)

	case st.phase == phaseCollecting && !sameSpeaker(st.pending, in):
		// A DIFFERENT PERSON. Coalescing exists for one person firing off
		// several bubbles in a row, and merging across senders quietly rewrote
		// who said what: the batch keeps the FIRST message's handle, so a
		// second person's words arrived under the first person's name.
		//
		// That is not cosmetic. The handle is what the owner check is derived
		// from, so the owner texting first and somebody else texting inside the
		// window made THEIR message owner-authoritative — past the premise
		// gate, out of the live-claim scope, and stored as the owner's own
		// account of themselves.
		//
		// So the batch closes and this message waits for a batch of its OWN.
		// One speaker per turn is an assumption the rest of the system makes.
		//
		// Not c.lead(key, …): that leads whatever is in the slot, which is the
		// OTHER speaker's batch. Two goroutines then drove one state, and the
		// loser found the slot already deleted and returned silently — so
		// refusing to merge dropped the message instead of merging it, which is
		// the worse of the two failures and the one nobody would report as a
		// bug. It just looks like the agent ignored you.
		st.deadline = time.Now() // stop extending; let its leader dispatch now
		c.mu.Unlock()
		return c.leadOwn(key, in, run, window)

	case st.phase == phaseCollecting:
		// A batch is still open: merge in and extend the window. The existing
		// leader will produce the reply, so this caller stays silent.
		st.pending = mergeInbound(st.pending, in)
		st.deadline = time.Now().Add(window)
		c.mu.Unlock()
		return ChannelReply{}, nil

	case st.phase != phaseCollecting && !sameSpeaker(st.pending, in):
		// Same rule while a turn is running: a different person does not get
		// folded into somebody else's turn, and the running turn is NOT
		// cancelled — it was started by someone whose message is still worth
		// answering. It finishes, then this one takes the slot.
		c.mu.Unlock()
		return c.leadOwn(key, in, run, window)

	default: // phaseRunning
		// A turn is already executing for this session. Fold this message into
		// what it was running, supersede it (bump gen so its result is
		// discarded), cancel it, and re-open the batch. This caller becomes the
		// new leader and drives the reprocess.
		st.pending = mergeInbound(st.pending, in)
		st.deadline = time.Now().Add(window)
		st.phase = phaseCollecting
		st.gen++
		if st.cancel != nil {
			go st.cancel()
		}
		c.mu.Unlock()
		return c.lead(key, run, window)
	}
}

// handoffPoll is how often a waiting speaker re-checks whether the session is
// free. Short enough that a person waiting on a reply cannot perceive it, long
// enough that a ten-minute turn does not spin a core.
const handoffPoll = 25 * time.Millisecond

// leadOwn waits for the session slot to empty, claims it for `in`, and leads
// that batch.
//
// This is what serializes a session across speakers. Two people in one group
// chat get two turns, one after the other, each carrying only its own speaker's
// words and handle — rather than one turn carrying both, or (the bug this
// replaced) one turn and one message that quietly went nowhere.
//
// Serializing rather than running both at once is deliberate: they share a
// session, so concurrent turns would interleave writes to one history and each
// would answer without seeing the other. A short wait is the cheaper mistake.
func (c *ChannelCoalescer) leadOwn(key string, in ChannelInbound, run ChannelAgentRunnerFunc, window time.Duration) (ChannelReply, error) {
	// Bounded by the same limit a dispatch gets: if the turn ahead is somehow
	// stuck, this takes the slot rather than waiting on it forever. A message
	// answered late beats a message never answered.
	deadline := time.Now().Add(channelDispatchTimeout)
	for {
		c.mu.Lock()
		st := c.sess[key]
		switch {
		case st == nil:
			c.sess[key] = &coalesceState{pending: in, deadline: time.Now().Add(window), phase: phaseCollecting}
			c.mu.Unlock()
			return c.lead(key, run, window)

		case st.phase == phaseCollecting && sameSpeaker(st.pending, in):
			// While waiting, someone from our own side opened a batch — this
			// belongs in it. Without this, a person's second bubble that landed
			// behind another speaker would start a THIRD turn instead of
			// joining the one it belongs to.
			st.pending = mergeInbound(st.pending, in)
			st.deadline = time.Now().Add(window)
			c.mu.Unlock()
			return ChannelReply{}, nil

		case time.Now().After(deadline):
			Log("[channel] session %q still busy after %s — taking the slot for %q", key, channelDispatchTimeout, in.Handle)
			if st.cancel != nil {
				go st.cancel()
			}
			c.sess[key] = &coalesceState{pending: in, deadline: time.Now().Add(window), phase: phaseCollecting}
			c.mu.Unlock()
			return c.lead(key, run, window)
		}
		c.mu.Unlock()
		time.Sleep(handoffPoll)
	}
}

// lead drives a batch: wait out the (possibly extended) window, dispatch the
// merged input once, then return its reply — unless a newer message superseded
// this dispatch mid-flight, in which case it discards its result and stays
// silent so the newer leader owns the reply.
func (c *ChannelCoalescer) lead(key string, run ChannelAgentRunnerFunc, window time.Duration) (ChannelReply, error) {
	for {
		c.mu.Lock()
		st := c.sess[key]
		if st == nil {
			// Ownership was taken and released by someone else; nothing to do.
			c.mu.Unlock()
			return ChannelReply{}, nil
		}
		if d := time.Until(st.deadline); d > 0 {
			// A follow-up extended the window: keep waiting.
			c.mu.Unlock()
			time.Sleep(d)
			continue
		}
		// Window elapsed: promote to running and dispatch the merged batch.
		in := st.pending
		myGen := st.gen
		st.phase = phaseRunning
		ctx, cancel := context.WithTimeout(context.Background(), channelDispatchTimeout)
		st.cancel = cancel
		c.mu.Unlock()

		reply, err := run(ctx, in)
		cancel()

		c.mu.Lock()
		st = c.sess[key]
		if st == nil || st.gen != myGen {
			// Superseded by a newer message that cancelled this dispatch; the
			// newer leader owns the reply. Drop this (aborted) result.
			c.mu.Unlock()
			return ChannelReply{}, nil
		}
		delete(c.sess, key)
		c.mu.Unlock()
		return reply, err
	}
}

// mergeInbound folds `add` into `base`: text joins (a question then a caption
// read as one message), attachments union, and the conversation identity stays
// base's (same sender within the window). The fuller roster and any non-nil
// status callback are preferred so nothing useful is lost in the merge.
// sameSpeaker reports whether two inbounds came from the same person.
//
// Decided on the HANDLE, which the transport supplies, never on the display
// name, which the sender chooses — merging on a name would let two people be
// treated as one by matching each other's.
//
// Empty handles compare equal: a transport that attributes nothing gives us
// nothing to tell them apart with, and on such a surface the coalescer behaves
// as it did before this rule existed.
func sameSpeaker(base, add ChannelInbound) bool {
	return strings.EqualFold(strings.TrimSpace(base.Handle), strings.TrimSpace(add.Handle))
}

func mergeInbound(base, add ChannelInbound) ChannelInbound {
	out := base
	out.MergedCount = messageCount(base) + messageCount(add)
	switch {
	case strings.TrimSpace(out.Text) == "":
		out.Text = add.Text
	case strings.TrimSpace(add.Text) != "":
		// A BLANK LINE, not a newline. Joined with "\n" the two bubbles read as
		// one wrapped thought, which is exactly how a second question ends up
		// unanswered; separated by a blank line they read as two, which is what
		// they are. The runner also states the count out of band — this only
		// makes the text itself honest about its own shape.
		out.Text = out.Text + "\n\n" + add.Text
	}
	out.Images = append(append([]string(nil), out.Images...), add.Images...)
	out.Videos = append(append([]string(nil), out.Videos...), add.Videos...)
	out.Audios = append(append([]string(nil), out.Audios...), add.Audios...)
	if out.StatusCallback == nil {
		out.StatusCallback = add.StatusCallback
	}
	if len(add.Roster) > len(out.Roster) {
		out.Roster = add.Roster
	}
	return out
}

// messageCount reads how many original messages an inbound stands for. An
// unmerged one counts as itself, so folding two singles gives two rather than
// zero.
func messageCount(in ChannelInbound) int {
	if in.MergedCount < 1 {
		return 1
	}
	return in.MergedCount
}
