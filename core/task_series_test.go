package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The whole point of the ledger: the count survives the turn that declared it.
// A model asked to carry "2 of 3" across a wake, several minutes and whatever
// the user said in between is a model that drops it quietly.
func TestASeriesCountsAcrossCallsAndClosesItself(t *testing.T) {
	const sess, tool = "sess-series-1", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })

	piece, of := AdvanceTaskSeries(sess, tool, 3)
	if piece != 1 || of != 3 {
		t.Fatalf("first piece of three = %d of %d, want 1 of 3", piece, of)
	}
	// The later calls carry no count — that is the shape the model actually
	// produces, and re-declaring it every time is bookkeeping it gets wrong.
	if piece, of = AdvanceTaskSeries(sess, tool, 0); piece != 2 || of != 3 {
		t.Fatalf("second piece = %d of %d, want 2 of 3", piece, of)
	}
	if piece, of = AdvanceTaskSeries(sess, tool, 0); piece != 3 || of != 3 {
		t.Fatalf("third piece = %d of %d, want 3 of 3", piece, of)
	}
	if TaskSeriesOpen(sess, tool) {
		t.Error("the last piece must close the series — an open one renumbers whatever renders next")
	}
	// And a call after it is over is a lone piece of work again, not piece 4.
	if piece, of = AdvanceTaskSeries(sess, tool, 0); piece != 0 || of != 0 {
		t.Errorf("a call after the series ended = %d of %d, want 0 of 0", piece, of)
	}
}

func TestALoneCallIsNotASeries(t *testing.T) {
	if piece, of := AdvanceTaskSeries("sess-series-2", "image", 1); piece != 0 || of != 0 {
		t.Errorf("one picture is not a set: got %d of %d", piece, of)
	}
	if TaskSeriesOpen("sess-series-2", "image") {
		t.Error("a single call must leave no series behind")
	}
}

// Clamped rather than refused: the number is the model's reading of "a few",
// and an error over it costs a round to correct something nobody cared about
// the exact value of.
func TestAnOverlongSeriesIsClampedNotRefused(t *testing.T) {
	const sess, tool = "sess-series-3", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })
	piece, of := AdvanceTaskSeries(sess, tool, 99)
	if piece != 1 {
		t.Fatalf("piece = %d, want 1", piece)
	}
	if of != taskSeriesMax() {
		t.Errorf("total = %d, want it clamped to %d", of, taskSeriesMax())
	}
}

// Two sets in one conversation are two sets. Sharing the count would let a
// finished repo scan advance an image series and tell the model to draw again.
func TestSeriesAreKeyedByTool(t *testing.T) {
	const sess = "sess-series-4"
	t.Cleanup(func() { CloseTaskSeries(sess, "image"); CloseTaskSeries(sess, "other") })
	if _, of := AdvanceTaskSeries(sess, "image", 3); of != 3 {
		t.Fatalf("image series total = %d, want 3", of)
	}
	if piece, of := AdvanceTaskSeries(sess, "other", 0); piece != 0 || of != 0 {
		t.Errorf("another tool must not inherit the count: got %d of %d", piece, of)
	}
	if piece, _ := AdvanceTaskSeries(sess, "image", 0); piece != 2 {
		t.Errorf("the image series must be where it was left: piece %d, want 2", piece)
	}
}

func TestAFailedPieceEndsTheSet(t *testing.T) {
	const sess, tool = "sess-series-5", "image"
	AdvanceTaskSeries(sess, tool, 3)
	CloseTaskSeries(sess, tool)
	if TaskSeriesOpen(sess, tool) {
		t.Error("a closed series must not still be open")
	}
	if piece, _ := AdvanceTaskSeries(sess, tool, 0); piece != 0 {
		t.Errorf("nothing should resume a closed set: got piece %d", piece)
	}
}

func TestAnAbandonedSeriesExpires(t *testing.T) {
	const sess, tool = "sess-series-6", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })
	AdvanceTaskSeries(sess, tool, 4)

	taskSeriesMu.Lock()
	taskSeriesLedger[taskSeriesKey(sess, tool)].at = time.Now().Add(-taskSeriesTTL - time.Minute)
	taskSeriesMu.Unlock()

	// The sweep runs on access, so an unrelated series is what triggers it.
	AdvanceTaskSeries("sess-series-6b", tool, 2)
	t.Cleanup(func() { CloseTaskSeries("sess-series-6b", tool) })
	if TaskSeriesOpen(sess, tool) {
		t.Error("a set the model walked away from must not outlive its TTL")
	}
}

func TestTheContinuationSaysWhereItIsAndStopsAtTheEnd(t *testing.T) {
	c := SeriesContinuation(1, 3, "another take on: a red bicycle")
	for _, want := range []string{"PIECE 1 OF 3", "same turn", "a red bicycle", SeriesContinuationMarker} {
		if !strings.Contains(c, want) {
			t.Errorf("the continuation must contain %q:\n%s", want, c)
		}
	}
	// The vocabulary the detach notice bans must not be handed back for the
	// model to repeat at the user.
	if !strings.Contains(c, "Do NOT mention jobs, queues") {
		t.Errorf("the continuation must ban narrating the machinery:\n%s", c)
	}
	// The ACT comes before the reply. Told to write and then act, the model
	// wrote "Starting the second variation now" and ended the turn — the set
	// stranded at 1 of 3 by a sentence that sounded like progress.
	act := strings.Index(c, "FIRST, before you write")
	reply := strings.Index(c, "THEN, in the same turn")
	if act < 0 || reply < 0 || act > reply {
		t.Errorf("the tool call must be asked for BEFORE the reply:\n%s", c)
	}
	if got := SeriesContinuation(3, 3, "x"); got != "" {
		t.Errorf("the last piece must ask for nothing more, got:\n%s", got)
	}
	if got := SeriesContinuation(1, 1, "x"); got != "" {
		t.Errorf("a lone piece is not a series, got:\n%s", got)
	}
}

// The instruction is one-shot. Left on the session it would ride out with a
// second, unrelated result and start a piece nobody asked for.
func TestAContinuationReachesExactlyOneTurn(t *testing.T) {
	sess := &ToolSession{}
	sess.SetTaskContinuation("start the next one")
	if got := sess.TakeTaskContinuation(); got != "start the next one" {
		t.Fatalf("first take = %q", got)
	}
	if got := sess.TakeTaskContinuation(); got != "" {
		t.Errorf("second take = %q, want it cleared", got)
	}
}

// The path that actually gets used. Told to make four variations the model
// does not declare four — it calls the tool four times, the way it would if
// nothing detached. The refusal is what notices.
func TestARefusedSecondCallBecomesASet(t *testing.T) {
	const sess, tool = "sess-series-7", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })

	if of := ExtendTaskSeries(sess, tool); of != 2 {
		t.Fatalf("a refused second call = set of %d, want 2 (the one running, and this one)", of)
	}
	// A third attempt in the same turn wanted three.
	if of := ExtendTaskSeries(sess, tool); of != 3 {
		t.Fatalf("a refused third call = set of %d, want 3", of)
	}
	// The piece already in flight then books itself as the first of them and
	// asks for the next — with no count ever passed by the model.
	piece, of := AdvanceTaskSeries(sess, tool, 0)
	if piece != 1 || of != 3 {
		t.Fatalf("the running piece = %d of %d, want 1 of 3", piece, of)
	}
	if SeriesContinuation(piece, of, "x") == "" {
		t.Error("a set built from refusals must still ask for its next piece")
	}
}

// Once a piece has landed, a second call in the same turn is the model
// mis-sequencing a set it already has — not asking for a bigger one.
func TestALandedPieceStopsTheSetFromGrowing(t *testing.T) {
	const sess, tool = "sess-series-8", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })
	AdvanceTaskSeries(sess, tool, 3) // piece 1 of 3 lands
	if of := ExtendTaskSeries(sess, tool); of != 3 {
		t.Errorf("set grew to %d after a piece landed; three variations must not become nine", of)
	}
}

func TestRefusalsCannotGrowASetPastTheCap(t *testing.T) {
	const sess, tool = "sess-series-9", "image"
	t.Cleanup(func() { CloseTaskSeries(sess, tool) })
	for i := 0; i < 50; i++ {
		ExtendTaskSeries(sess, tool)
	}
	if of := ExtendTaskSeries(sess, tool); of != taskSeriesMax() {
		t.Errorf("set = %d, want it capped at %d", of, taskSeriesMax())
	}
}

// The refusal changes character once there IS a set: "you already did this"
// is the reading that made the model go looking for another route.
func TestTheRefusalTellsTheModelTheRestIsBooked(t *testing.T) {
	notice := secondDetachNotice("image", TaskRun{ID: "task-1"}, 3)
	for _, want := range []string{"set of 3", "told EARLY"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice must say the rest is arranged (%q):\n%s", want, notice)
		}
	}
	if strings.Contains(notice, "Say how many you intend on the FIRST call") {
		t.Error("no point asking for a count once the set already exists")
	}
}

// A tool that says its work is background work outranks the clock. Without
// this the decision is duration-only, and a twenty-second render can never be
// backgrounded no matter how much the conversation would prefer it.
func TestAToolCanDeclareItsWorkBackgroundWork(t *testing.T) {
	saved := TaskRunnerFunc
	TaskRunnerFunc = func(*ToolSession, string, func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		return TaskRun{ID: "t"}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })

	sess := &ToolSession{}
	quick := &fakeAlwaysDetachTool{expected: 2 * time.Second, always: true}
	if _, detach := ShouldDetach(quick, nil, sess); !detach {
		t.Error("a tool that declares background work must detach on a two-second estimate")
	}
	quick.always = false
	if _, detach := ShouldDetach(quick, nil, sess); detach {
		t.Error("with the declaration off, the duration rule must decide again")
	}
}

type fakeAlwaysDetachTool struct {
	expected time.Duration
	always   bool
}

func (f *fakeAlwaysDetachTool) Name() string { return "fake_render" }
func (f *fakeAlwaysDetachTool) Desc() string { return "test tool" }
func (f *fakeAlwaysDetachTool) Params() map[string]ToolParam {
	return map[string]ToolParam{}
}
func (f *fakeAlwaysDetachTool) Run(map[string]any) (string, error) { return "", nil }
func (f *fakeAlwaysDetachTool) ExpectedDuration(map[string]any, *ToolSession) time.Duration {
	return f.expected
}
func (f *fakeAlwaysDetachTool) AlwaysDetach(map[string]any, *ToolSession) bool { return f.always }

// The same call, judged by where the conversation is. A web chat shows a live
// pill and a progress tree; a texted conversation shows nothing at all until a
// reply arrives, so the same wait reads as an assistant that went away.
func TestTheDetachThresholdFollowsTheSurface(t *testing.T) {
	watched := &ToolSession{}
	texting := &ToolSession{ChannelChatID: "iMessage;-;+14155551234"}

	if watched.Unattended() {
		t.Error("a web chat is watched")
	}
	if !texting.Unattended() {
		t.Error("a messaging conversation shows nothing until a reply arrives")
	}
	if !(&ToolSession{ChannelHandle: "+14155551234"}).Unattended() {
		t.Error("a handle names a conversation just as a chat id does")
	}
	if got := taskDetachThreshold(texting); got >= taskDetachThreshold(watched) {
		t.Errorf("messaging threshold %s must sit below the watched one %s", got, taskDetachThreshold(watched))
	}
	if taskDetachThreshold(nil) != taskDetachThreshold(watched) {
		t.Error("no session must fall back to the ordinary threshold")
	}
}

// A call too quick to detach in a watched chat still detaches on a channel —
// which is the whole point of the split.
func TestAMediumCallDetachesOnAChannelAndNotInChat(t *testing.T) {
	saved := TaskRunnerFunc
	TaskRunnerFunc = func(*ToolSession, string, func(context.Context) (TaskProduct, error)) (TaskRun, error) {
		return TaskRun{ID: "t"}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = saved })

	// Between the two thresholds: longer than a texter should wait in silence,
	// shorter than a watched chat bothers to detach for.
	tool := &fakeAlwaysDetachTool{expected: 90 * time.Second}
	if _, detach := ShouldDetach(tool, nil, &ToolSession{}); detach {
		t.Error("a watched chat can show 90s of work happening — no need to detach")
	}
	if _, detach := ShouldDetach(tool, nil, &ToolSession{ChannelChatID: "c"}); !detach {
		t.Error("90s of silence on a text thread is an assistant that looks gone")
	}
}

// One slot and one set across every name for the same act. Rationed by NAME,
// two render tools ran two jobs for one request and a set forked mid-flight.
func TestRenderToolsShareOneSlotAndOneSet(t *testing.T) {
	sess := &ToolSession{ChatSessionID: "sess-identity"}
	t.Cleanup(func() { CloseTaskSeries("sess-identity", RenderDetachIdentity) })

	grouped := DetachPolicy{Tool: "image", Key: RenderDetachIdentity}
	twin := DetachPolicy{Tool: "generate_image_comfyui", Key: RenderDetachIdentity}

	if _, free := sess.ClaimDetachSlot(grouped.key()); !free {
		t.Fatal("the first render must get the slot")
	}
	if _, free := sess.ClaimDetachSlot(twin.key()); free {
		t.Error("a second render under another name must not get a second slot — that delivers the same request twice")
	}
	// And the set they build is one set: begun under one name, continued under
	// the other, still counting to the same total.
	if of := ExtendTaskSeries(sess.ChatSessionID, twin.key()); of != 2 {
		t.Fatalf("set = %d, want 2", of)
	}
	if piece, of := AdvanceTaskSeries(sess.ChatSessionID, grouped.key(), 0); piece != 1 || of != 2 {
		t.Errorf("piece %d of %d — the set must not fork across names", piece, of)
	}
	// A tool with no declared identity is still rationed on its own name.
	if k := (DetachPolicy{Tool: "video"}).key(); k != "video" {
		t.Errorf("key = %q, want the tool's own name", k)
	}
}

// The piece is booked only for work that outlives the turn. An inline call has
// its own next round, and counting it there is what made a set of three tell
// the model to keep going forever.
func TestOnlyDetachedWorkBooksAPiece(t *testing.T) {
	inline := &ToolSession{ChatSessionID: "sess-book-inline"}
	if piece, of := BookSeriesPiece(inline, RenderDetachIdentity, 3, "x"); piece != 0 || of != 0 {
		t.Errorf("inline booked %d of %d, want nothing", piece, of)
	}
	if TaskSeriesOpen("sess-book-inline", RenderDetachIdentity) {
		t.Error("an inline call must not open a set")
	}

	detached := &ToolSession{ChatSessionID: "sess-book-detached", Detached: true}
	t.Cleanup(func() { CloseTaskSeries("sess-book-detached", RenderDetachIdentity) })
	piece, of := BookSeriesPiece(detached, RenderDetachIdentity, 3, "a red bicycle")
	if piece != 1 || of != 3 {
		t.Fatalf("booked %d of %d, want 1 of 3", piece, of)
	}
	if c := detached.TakeTaskContinuation(); c == "" {
		t.Error("a set with pieces left must leave the instruction that starts the next")
	}
}

// The silent fallback that hid a whole class of failure: a call that cannot
// detach runs inline instead, which is right as a fallback and invisible as a
// diagnosis. A caller with no task host must therefore still WORK — just not
// in the background.
func TestACallThatCannotDetachStillRuns(t *testing.T) {
	saved := TaskRunnerFunc
	TaskRunnerFunc = nil // no host — exactly what a wake turn had when it could not resolve its run
	t.Cleanup(func() { TaskRunnerFunc = saved })

	p := DetachPolicy{
		Tool:     "image",
		Expected: func(map[string]any, *ToolSession) time.Duration { return time.Hour },
		Detached: func(map[string]any, *ToolSession) (string, error) { return "", nil },
	}
	if shouldDetachPolicy(p, nil, &ToolSession{ChatSessionID: "s"}) {
		t.Error("nothing can detach without a host to run it")
	}
	ran := false
	h := WrapDetachable(p, &ToolSession{ChatSessionID: "s"}, func(map[string]any) (string, error) {
		ran = true
		return "done inline", nil
	})
	out, err := h(map[string]any{})
	if err != nil || out != "done inline" || !ran {
		t.Errorf("the inline fallback must still do the work: out=%q err=%v ran=%v", out, err, ran)
	}
}

// Where the work RAN and where its result BELONGS are two facts, and delivery
// has to use the second. A wake or scheduled fire runs under "scheduled:<real>"
// so its ephemeral tool state stays off the user's thread — and a picture
// started from one of those turns was then delivered into "scheduled:<real>",
// a conversation nobody is looking at.
func TestAResultIsDeliveredToTheConversationNotTheSubSession(t *testing.T) {
	fire := &ToolSession{ChatSessionID: "scheduled:real-1", DeliverySessionID: "real-1"}
	if got := fire.DeliverySession(); got != "real-1" {
		t.Errorf("delivery session = %q, want the conversation the user is in", got)
	}
	// An ordinary turn has only one id and must be unaffected.
	if got := (&ToolSession{ChatSessionID: "real-1"}).DeliverySession(); got != "real-1" {
		t.Errorf("plain session = %q, want its own id", got)
	}
	if got := (&ToolSession{}).DeliverySession(); got != "" {
		t.Errorf("empty session = %q, want empty", got)
	}
	// And it survives into the detached session, which is the only thing left
	// holding it by the time delivery happens.
	d := fire.ForDetachedTask(nil)
	if got := d.DeliverySession(); got != "real-1" {
		t.Errorf("detached delivery session = %q — the target must outlive the turn", got)
	}
}

// The SET is keyed the same way, and for the same reason: a set opened by the
// interactive turn under "real-1" must be the one a wake turn continues, not a
// second set under "scheduled:real-1" that starts counting from one again.
func TestASetSurvivesTheWakeTurnsOwnSessionID(t *testing.T) {
	t.Cleanup(func() { CloseTaskSeries("real-2", RenderDetachIdentity) })
	live := &ToolSession{ChatSessionID: "real-2", Detached: true}
	if piece, of := BookSeriesPiece(live, RenderDetachIdentity, 3, "x"); piece != 1 || of != 3 {
		t.Fatalf("opened %d of %d, want 1 of 3", piece, of)
	}
	wake := &ToolSession{ChatSessionID: "scheduled:real-2", DeliverySessionID: "real-2", Detached: true}
	if piece, of := BookSeriesPiece(wake, RenderDetachIdentity, 0, "x"); piece != 2 || of != 3 {
		t.Errorf("wake booked %d of %d, want 2 of 3 — the set must not fork per turn", piece, of)
	}
}

// Stopping a background job has to stop the SET it belonged to, or the button
// lies: kill the second of four renders and the next wake cheerfully starts
// the third, because the ledger still says there are pieces left.
func TestCancellingClosesEverySetInTheConversation(t *testing.T) {
	AdvanceTaskSeries("sess-cancel", RenderDetachIdentity, 4)
	AdvanceTaskSeries("sess-cancel", "video", 3)
	AdvanceTaskSeries("sess-other", RenderDetachIdentity, 4)
	t.Cleanup(func() { CloseTaskSeriesForSession("sess-other") })

	if n := CloseTaskSeriesForSession("sess-cancel"); n != 2 {
		t.Errorf("closed %d sets, want both of them", n)
	}
	if TaskSeriesOpen("sess-cancel", RenderDetachIdentity) || TaskSeriesOpen("sess-cancel", "video") {
		t.Error("a cancelled conversation must have no set still counting")
	}
	// Another conversation's work is untouched — cancel means this one.
	if !TaskSeriesOpen("sess-other", RenderDetachIdentity) {
		t.Error("cancelling one conversation must not stop another's")
	}
	if n := CloseTaskSeriesForSession(""); n != 0 {
		t.Errorf("empty session closed %d sets, want 0", n)
	}
}
