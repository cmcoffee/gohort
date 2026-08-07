package core

// The end-to-end question, and the one actually asked: in a group chat where
// two people talk over each other, can a batch ever carry one person's words
// under another person's handle?
//
// sameSpeaker is unit-tested elsewhere and answers a narrower question — does
// the comparison work. These drive the real Dispatch path with two senders
// inside one window, because the failure being chased was never in the
// comparison. It was in what the state machine did once the answer was "no".

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// coalesceWindowForTest shortens the hold so a two-sender race resolves in
// milliseconds rather than seconds, and restores it afterwards.
func coalesceWindowForTest(t *testing.T, ms float64) {
	t.Helper()
	tunablesMu.Lock()
	prev, had := tunCache["tune_channel_coalesce_ms"]
	tunCache["tune_channel_coalesce_ms"] = ms
	tunablesMu.Unlock()
	t.Cleanup(func() {
		tunablesMu.Lock()
		if had {
			tunCache["tune_channel_coalesce_ms"] = prev
		} else {
			delete(tunCache, "tune_channel_coalesce_ms")
		}
		tunablesMu.Unlock()
	})
}

// dispatchRecorder captures what each turn was actually handed.
type dispatchRecorder struct {
	mu   sync.Mutex
	runs []ChannelInbound
}

func (d *dispatchRecorder) run(ctx context.Context, in ChannelInbound) (ChannelReply, error) {
	d.mu.Lock()
	d.runs = append(d.runs, in)
	d.mu.Unlock()
	return ChannelReply{Text: "ok"}, nil
}

func (d *dispatchRecorder) seen() []ChannelInbound {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ChannelInbound(nil), d.runs...)
}

func TestTwoSendersNeverShareATurn(t *testing.T) {
	coalesceWindowForTest(t, 60)
	c := NewChannelCoalescer()
	rec := &dispatchRecorder{}

	const (
		ownerSays = "the deploy went out clean"
		otherSays = "he never tested it"
	)
	owner := ChannelInbound{Handle: "+15550100", SenderName: "Craig", Text: ownerSays}
	other := ChannelInbound{Handle: "+15550199", SenderName: "Craig", Text: otherSays}

	// Same display name on purpose. If anything in this path still compares
	// names, these two are indistinguishable and the bug reappears.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = c.Dispatch("s1", owner, rec.run) }()
	// Staggered inside the window: the second arrives while the first is still
	// collecting, which is the exact interleaving that used to merge them.
	time.Sleep(15 * time.Millisecond)
	go func() { defer wg.Done(); _, _ = c.Dispatch("s1", other, rec.run) }()
	wg.Wait()

	runs := rec.seen()
	if len(runs) != 2 {
		t.Fatalf("two speakers must produce two turns, got %d: %+v", len(runs), runs)
	}
	for _, in := range runs {
		// The load-bearing assertion: no turn holds both people's words.
		if strings.Contains(in.Text, ownerSays) && strings.Contains(in.Text, otherSays) {
			t.Errorf("a turn merged two speakers: %q from %q", in.Text, in.Handle)
		}
		// And the handle on the turn is the handle of whoever said it — this is
		// what the owner check reads, so getting it wrong hands a stranger the
		// owner's authority.
		switch {
		case strings.Contains(in.Text, ownerSays):
			if in.Handle != owner.Handle {
				t.Errorf("the owner's words arrived under %q", in.Handle)
			}
		case strings.Contains(in.Text, otherSays):
			if in.Handle != other.Handle {
				t.Errorf("the other speaker's words arrived under %q", in.Handle)
			}
		default:
			t.Errorf("a turn ran with text belonging to nobody: %q", in.Text)
		}
	}
	// Neither may be silently dropped. Refusing to merge is only correct if the
	// message that could not be merged still gets answered.
	var sawOwner, sawOther int
	for _, in := range runs {
		if strings.Contains(in.Text, ownerSays) {
			sawOwner++
		}
		if strings.Contains(in.Text, otherSays) {
			sawOther++
		}
	}
	if sawOwner != 1 || sawOther != 1 {
		t.Errorf("each message must be dispatched exactly once, got owner=%d other=%d", sawOwner, sawOther)
	}
}

// The same rule once a turn is already executing. A message from someone else
// arriving mid-turn must not be folded into the running one — and must not
// vanish because the slot was busy.
func TestASecondSpeakerMidTurnIsNotFoldedIn(t *testing.T) {
	coalesceWindowForTest(t, 30)
	c := NewChannelCoalescer()
	rec := &dispatchRecorder{}

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	slow := func(ctx context.Context, in ChannelInbound) (ChannelReply, error) {
		reply, err := rec.run(ctx, in)
		once.Do(func() { close(started); <-release })
		return reply, err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Dispatch("s2", ChannelInbound{Handle: "+15550100", Text: "restarting it now"}, slow)
	}()
	<-started // the first turn is running

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Dispatch("s2", ChannelInbound{Handle: "+15550199", Text: "don't, I'm on the box"}, slow)
	}()
	time.Sleep(40 * time.Millisecond)
	close(release)
	wg.Wait()

	runs := rec.seen()
	if len(runs) != 2 {
		t.Fatalf("a mid-turn message from someone else needs its own turn, got %d: %+v", len(runs), runs)
	}
	for _, in := range runs {
		if strings.Contains(in.Text, "restarting") && strings.Contains(in.Text, "on the box") {
			t.Errorf("a running turn absorbed another speaker: %q", in.Text)
		}
	}
}

// Coalescing must still do its job for one person: the refusal is about
// speakers, not about batching, and a rule that quietly disabled merging would
// pass every assertion above.
func TestOneSpeakerStillCoalesces(t *testing.T) {
	coalesceWindowForTest(t, 60)
	c := NewChannelCoalescer()
	rec := &dispatchRecorder{}

	var wg sync.WaitGroup
	send := func(text string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Dispatch("s3", ChannelInbound{Handle: "+15550100", Text: text}, rec.run)
		}()
	}
	send("what's the deploy status?")
	time.Sleep(10 * time.Millisecond)
	send("also check disk")
	wg.Wait()

	runs := rec.seen()
	if len(runs) != 1 {
		t.Fatalf("one speaker's two bubbles are one turn, got %d: %+v", len(runs), runs)
	}
	if !strings.Contains(runs[0].Text, "deploy status") || !strings.Contains(runs[0].Text, "check disk") {
		t.Errorf("the batch lost a message: %q", runs[0].Text)
	}
	if runs[0].MergedCount != 2 {
		t.Errorf("merged count = %d, want 2", runs[0].MergedCount)
	}
}
