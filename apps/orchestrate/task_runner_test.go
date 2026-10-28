// Delivering what a background task produced. The text half always worked; the
// files it made are the half that went missing, and these pin the staging that
// carries them from the finished task to the turn that announces it.
package orchestrate

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

func TestStagedAttachmentsRideTheTurnThatAnnouncesThem(t *testing.T) {
	const session = "sess-stage-1"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })

	if n := stageTaskAttachments(session, TaskProduct{Text: "done", Images: []string{"PICTURE"}}); n != 1 {
		t.Fatalf("staged %d, want 1", n)
	}
	if !hasStagedAttachments(session) {
		t.Fatal("a staged delivery must be visible to the wake router — otherwise it joins a live turn and strands the file")
	}

	sess := &ToolSession{}
	if n := claimTaskAttachments(session, sess); n != 1 {
		t.Fatalf("claimed %d, want 1", n)
	}
	// Folded into the session, not handed back as a separate list: that is the
	// one place every surface already collects outbound attachments from.
	if len(sess.Images) != 1 || sess.Images[0] != "PICTURE" {
		t.Fatalf("attachment did not reach the turn's session: %#v", sess.Images)
	}
	// Claimed once. A second turn must not re-send the same picture.
	if hasStagedAttachments(session) {
		t.Error("staged attachments must be consumed by the claim")
	}
	if n := claimTaskAttachments(session, &ToolSession{}); n != 0 {
		t.Errorf("second claim returned %d, want 0", n)
	}
}

func TestSeveralFinishedTasksCoalesceTheirFiles(t *testing.T) {
	// Their notes already merge into one turn (joinWakeNotes); their files have
	// to merge with them, or the second render is announced and never sent.
	const session = "sess-stage-2"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })

	stageTaskAttachments(session, TaskProduct{Images: []string{"FIRST"}})
	stageTaskAttachments(session, TaskProduct{Images: []string{"SECOND"}})

	sess := &ToolSession{}
	if n := claimTaskAttachments(session, sess); n != 2 {
		t.Fatalf("claimed %d, want both", n)
	}
	if len(sess.Images) != 2 || sess.Images[0] != "FIRST" || sess.Images[1] != "SECOND" {
		t.Fatalf("order or contents wrong: %#v", sess.Images)
	}
}

func TestOnlyAWakeTurnClaimsStagedFiles(t *testing.T) {
	// The gate on the claim. A recurring fire — or any other turn on the same
	// session — must not pick up a delivery meant for the wake, or a picture
	// goes out attached to something unrelated the user said.
	if !isTaskWake(taskWakeMarker + " The work you started earlier is done: image edit.") {
		t.Error("a wake note must be recognized as one")
	}
	if isTaskWake("[SCHEDULED UPDATE — fire 3, every 30m] check the feed") {
		t.Error("a scheduled fire must not claim a background task's files")
	}
	if isTaskWake("") {
		t.Error("an empty prompt is not a wake")
	}
}

func TestNothingStagedStagesNothing(t *testing.T) {
	const session = "sess-stage-3"
	if n := stageTaskAttachments(session, TaskProduct{Text: "just text"}); n != 0 {
		t.Errorf("staged %d for a text-only result, want 0", n)
	}
	if hasStagedAttachments(session) {
		t.Error("a text-only result must leave the wake free to join a live turn")
	}
}

func TestAWakeKeepsTheFactAndNotTheInstruction(t *testing.T) {
	// The thread has to keep what came back — the handle the user's next
	// message ("make it brighter") refers to. It must NOT keep "deliver this
	// now": an instruction left in history is one the model can read three
	// turns later and obey a second time.
	note := buildWakeNote("sess-note-1", "image edit: a cat", TaskProduct{
		Text: "The finished picture IS ATTACHED to this result. image#1 is its lasting handle.",
	}, nil)

	if !strings.Contains(note.history, "image#1") {
		t.Errorf("the handle naming the result must survive into history:\n%s", note.history)
	}
	if !strings.Contains(note.history, "image edit: a cat") {
		t.Errorf("history must say WHICH request this answers:\n%s", note.history)
	}
	if strings.Contains(note.history, "Deliver this to the user now") {
		t.Errorf("a one-turn instruction must not be persisted:\n%s", note.history)
	}
	// The delivering turn still gets told what to do.
	if !strings.Contains(note.prompt, "Deliver this to the user now") {
		t.Errorf("the prompt must carry the instruction:\n%s", note.prompt)
	}
	if !isTaskWake(note.prompt) || !isTaskWake(note.history) {
		t.Error("both halves must be recognizable as a task wake")
	}
}

func TestAFailedTaskSaysSoWithoutRetryingItself(t *testing.T) {
	note := buildWakeNote("sess-note-2", "image edit", TaskProduct{}, errTestTaskFailed)
	if !strings.Contains(note.prompt, errTestTaskFailed.Error()) {
		t.Errorf("the failure reason must reach the model:\n%s", note.prompt)
	}
	if !strings.Contains(note.prompt, "Do not silently retry") {
		t.Errorf("a failed render must not be quietly re-run:\n%s", note.prompt)
	}
}

var errTestTaskFailed = errors.New("the backend never returned an image")

func TestJoinedWakesMergeBothHalves(t *testing.T) {
	got := joinWakeNotes([]wakeNote{
		{prompt: "first prompt", history: "first fact"},
		{prompt: "second prompt", history: "second fact"},
	})
	for _, want := range []string{"first prompt", "second prompt"} {
		if !strings.Contains(got.prompt, want) {
			t.Errorf("merged prompt missing %q: %s", want, got.prompt)
		}
	}
	for _, want := range []string{"first fact", "second fact"} {
		if !strings.Contains(got.history, want) {
			t.Errorf("merged history missing %q: %s", want, got.history)
		}
	}
	// One result stays exactly itself — no numbering, no restructuring.
	solo := joinWakeNotes([]wakeNote{{prompt: "only", history: "fact"}})
	if solo.prompt != "only" || solo.history != "fact" {
		t.Errorf("a single note must pass through unchanged: %+v", solo)
	}
}

func TestOneFinishedTaskIsDeliveredOnce(t *testing.T) {
	// The double-announcement bug: the first result opened a coalescing window
	// AND was buffered in it, so its caller sent it, the window closed, and it
	// went out a second time fifteen seconds later — the second message
	// promising a picture the first had already carried off.
	const session = "sess-window-1"
	t.Cleanup(func() { takeBufferedWakes(session) })

	first := wakeNote{prompt: "first", history: "first fact"}
	if !claimWakeWindow(session, first) {
		t.Fatal("the first result must own delivery and send its own note")
	}
	if got := takeBufferedWakes(session); len(got) != 0 {
		t.Fatalf("the owner's note must NOT be buffered — it is being sent now (%d queued)", len(got))
	}
}

func TestResultsLandingInTheWindowGoOutTogether(t *testing.T) {
	const session = "sess-window-2"
	t.Cleanup(func() { takeBufferedWakes(session) })

	if !claimWakeWindow(session, wakeNote{prompt: "owner"}) {
		t.Fatal("first caller should own the window")
	}
	for _, n := range []string{"second", "third"} {
		if claimWakeWindow(session, wakeNote{prompt: n}) {
			t.Fatalf("%q arrived inside an open window — it must not open its own", n)
		}
	}
	notes := takeBufferedWakes(session)
	if len(notes) != 2 || notes[0].prompt != "second" || notes[1].prompt != "third" {
		t.Fatalf("window collected %+v, want the two siblings in order", notes)
	}
	// Closed: the next result opens a fresh window.
	if !claimWakeWindow(session, wakeNote{prompt: "later"}) {
		t.Error("a result after the window closed must own its own delivery")
	}
}

// A set still running says so to the turn that delivers this piece, and to
// nobody afterwards. Persisted, "start the next one" is an instruction a later
// turn can read again and obey — which is how a set of three acquires a fourth.
func TestAContinuationDrivesTheDeliveringTurnOnly(t *testing.T) {
	session := "sess-note-series"
	t.Cleanup(func() { claimTaskAttachments(session, &ToolSession{}) })
	note := buildWakeNote(session, "image generate: a red bicycle", TaskProduct{
		Text:         "Stored. image#1 is its lasting handle.",
		Images:       []string{"AAAA"},
		Continuation: SeriesContinuation(1, 3, "another take on: a red bicycle"),
	}, nil)

	if !strings.Contains(note.prompt, "PIECE 1 OF 3") {
		t.Errorf("the delivering turn must be told to start the next piece:\n%s", note.prompt)
	}
	if strings.Contains(note.history, "PIECE 1 OF 3") {
		t.Errorf("a one-turn instruction must not be persisted:\n%s", note.history)
	}
	// The fact half is unchanged: the handle still has to survive.
	if !strings.Contains(note.history, "image#1") {
		t.Errorf("the handle must still reach history:\n%s", note.history)
	}
}

// A failed piece ends the set. The wake for a failure already says not to retry
// silently, and carrying a continuation past a visible break keeps sending
// pictures for a request that went wrong.
func TestAFailedPieceCarriesNoContinuation(t *testing.T) {
	note := buildWakeNote("sess-note-series-failed", "image generate", TaskProduct{
		Continuation: SeriesContinuation(1, 3, "another take"),
	}, errTestTaskFailed)
	if strings.Contains(note.prompt, "PIECE 1 OF 3") {
		t.Errorf("a failure must not start the next piece:\n%s", note.prompt)
	}
}

// A result says which request it answers by quoting the message that asked:
// with several jobs out, labels read off the image prompt were too alike to
// tell apart. The quote is kept in the thread's record, since "which one was
// that" is a question a later turn has to answer too, and a failure carries it
// as well.
func TestAWakeQuotesTheMessageThatAskedForIt(t *testing.T) {
	asked := "put the dog\non a beach"
	note := buildWakeNote("sess-note-req", "image edit: All three men from the three reference pictures", TaskProduct{
		Text: "image#4 is its lasting handle.", Request: asked,
	}, nil)
	const quoted = `They asked for it with: "put the dog on a beach"`
	if !strings.Contains(note.prompt, quoted) || !strings.Contains(note.history, quoted) {
		t.Errorf("the note should quote the request on one line:\n%s", note.prompt)
	}
	if !strings.Contains(note.prompt, "as the answer to the message quoted above and to no other") {
		t.Errorf("the delivering turn should be told to answer that message:\n%s", note.prompt)
	}
	failed := buildWakeNote("sess-note-req2", "image edit", TaskProduct{Request: asked}, errTestTaskFailed)
	if !strings.Contains(failed.prompt, quoted) || !strings.Contains(failed.prompt, "Do not silently retry") {
		t.Errorf("a failure should name its request and still not retry:\n%s", failed.prompt)
	}
	if long := wakeRequestQuote(strings.Repeat("a", 400)); len([]rune(long)) != 303 {
		t.Errorf("a long request should be clipped to 300 characters: %d", len([]rune(long)))
	}
	if wakeRequestQuote("  \n ") != "" {
		t.Error("no request, no quote")
	}
}

// A held step's picture waits apart from what a wake carries out, and the wake
// sends it only when it starts no further pass: a chain that goes on sends
// nothing for the step, and one that stops sends where it got to rather than
// nothing at all.
func TestAHeldStepIsSentOnlyWhenTheChainStops(t *testing.T) {
	const sid = "sess-held-step"
	stageHeldImages(sid, []string{"c3RlcA=="})
	stageTaskAttachments(sid, TaskProduct{Images: []string{"ZG9uZQ=="}})
	if held := claimHeldImages(sid); len(held) != 1 || held[0] != "c3RlcA==" {
		t.Fatalf("held = %q", held)
	}
	carried := &ToolSession{}
	if n := claimTaskAttachments(sid, carried); n != 1 || carried.Images[0] != "ZG9uZQ==" {
		t.Errorf("a held picture should not ride out as an attachment: %d, %q", n, carried.Images)
	}

	// The wake started the next pass: this turn posts nothing.
	going := &fireRun{p: orchUpdatePayload{SessionID: sid}, subSess: &ToolSession{}, held: []string{"c3RlcA=="}}
	going.subSess.ClaimDetachSlot(RenderDetachIdentity)
	if !going.sendHeldIfStalled() || len(going.subSess.Images) != 0 {
		t.Errorf("a chain that goes on should post nothing for the step: %d image(s)", len(going.subSess.Images))
	}
	// It started none: the step goes out, with a line if the turn wrote none.
	stopped := &fireRun{p: orchUpdatePayload{SessionID: sid}, subSess: &ToolSession{}, held: []string{"c3RlcA=="}}
	if stopped.sendHeldIfStalled() || len(stopped.subSess.Images) != 1 || stopped.reply == "" || stopped.carriedAttachments != 1 {
		t.Errorf("a chain that stops should send its step: %d image(s), reply %q", len(stopped.subSess.Images), stopped.reply)
	}
	if (&fireRun{subSess: &ToolSession{}}).sendHeldIfStalled() {
		t.Error("a wake with nothing held is not a held step")
	}
}

// The wake says which pictures went into the result. Observed 2026-10-24: a
// four-photo blend stopped after two passes, and its reply described the
// fourth photo as being in the picture.
func TestAWakeSaysWhatThePictureWasMadeFrom(t *testing.T) {
	note := buildWakeNote("sess-note-inputs", "image edit", TaskProduct{
		Text:   "The finished picture IS ATTACHED.",
		Inputs: []string{"image#r.66c18084", "find-jelly.jpg"},
	}, nil)
	if !strings.Contains(note.history, "made from 2 picture(s): image#r.66c18084; find-jelly.jpg") {
		t.Errorf("the wake should name the inputs:\n%s", note.prompt)
	}
}

// A picture kept back because a pass built on it is superseded by that pass's
// result, and sent in its place when the pass fails.
func TestAKeptBackPictureGoesOutOnlyWhenThePassFails(t *testing.T) {
	buildWakeNote("sess-carry-ok", "image edit", TaskProduct{Images: []string{"ZmluYWw="}, Carried: []string{"c3RlcA=="}}, nil)
	if held := claimHeldImages("sess-carry-ok"); len(held) != 0 {
		t.Errorf("a finished pass supersedes the step it built on: %q", held)
	}
	claimTaskAttachments("sess-carry-ok", &ToolSession{})
	buildWakeNote("sess-carry-fail", "image edit", TaskProduct{Carried: []string{"c3RlcA=="}}, errTestTaskFailed)
	if held := claimHeldImages("sess-carry-fail"); len(held) != 1 || held[0] != "c3RlcA==" {
		t.Errorf("a failed pass should send the step it built on: %q", held)
	}
}

// What a wake carries is offered as a step, except a piece of a set.
func TestAWakeOffersItsPictureAsAStepExceptInASet(t *testing.T) {
	sess := &ToolSession{}
	sess.AppendImage("cGFpcg==")
	offerWakeSteps(sess, taskWakeMarker+" done", []string{"cGFpcg=="})
	if got := sess.WithdrawSteps(); len(got) != 1 {
		t.Errorf("a finished pass should be offered as a step: %q", got)
	}
	set := &ToolSession{}
	set.AppendImage("cGllY2U=")
	offerWakeSteps(set, taskWakeMarker+" done\n"+SeriesContinuationMarker+" 1 of 3", []string{"cGllY2U="})
	if got := set.WithdrawSteps(); len(got) != 0 || len(set.Images) != 1 {
		t.Error("a piece of a set is sent as it comes")
	}
}

// Delivering a finished background result back to the conversation that asked.
// The wake rides the scheduled-fire path, which APPENDS to a stored session and
// stops — right for a recurring task posting into a web thread, and useless
// when the conversation lives on a phone.

func TestAPerContactThreadResolvesToItsConversation(t *testing.T) {
	// "chan:<address>" carries the recipient in the session id.
	chatID, _, ok := channelTargetForWake("alice", "agent-1", "chan:any;+;chat872212368359368118")
	if !ok {
		t.Fatal("a per-contact channel thread must resolve to a recipient")
	}
	if chatID != "any;+;chat872212368359368118" {
		t.Errorf("chat id = %q, want the address from the session id", chatID)
	}
}

func TestAWebSessionIsNotSentAnywhere(t *testing.T) {
	// A plain web session needs no send — the user is looking at the thread, and
	// a message to a conversation that isn't one would be a misroute.
	if _, _, ok := channelTargetForWake("alice", "agent-1", "8cf98a5d-ed40-483a-bcea-e3b6f3c2fdf0"); ok {
		t.Error("a web session must not resolve to a messaging recipient")
	}
	if _, _, ok := channelTargetForWake("alice", "agent-1", ""); ok {
		t.Error("an empty session resolves to nothing")
	}
}

func TestACortexHomeWithNoSingleChannelStaysPut(t *testing.T) {
	// The cortex home IS the channel thread for a ONE-channel agent. With none
	// bound — or several — there is no single right recipient, and guessing
	// would send a private result to the wrong conversation.
	if _, _, ok := channelTargetForWake("alice", "agent-with-no-channels", cortexSessionID("agent-with-no-channels")); ok {
		t.Error("an agent with no bound channel has nowhere to deliver")
	}
}

// voiceThreads is a transport that can send a voice note, recording what it
// was handed.
type voiceThreads struct {
	mu   *sync.Mutex
	sent *[]voiceSend
}

type voiceSend struct{ chatID, text, voice string }

func (voiceThreads) Threads(string) []ChannelThreadInfo         { return nil }
func (voiceThreads) Messages(string, string, int) []ChannelLine { return nil }
func (voiceThreads) Members(string, string) []ChannelMember     { return nil }
func (v voiceThreads) Deliver(_, _, chatID, _, text, _ string, _ []string) error {
	return v.DeliverVoice("", "", chatID, "", text, "", nil, nil, "")
}
func (v voiceThreads) DeliverVoice(_, _, chatID, _, text, _ string, _, _ []string, voice string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	*v.sent = append(*v.sent, voiceSend{chatID, text, voice})
	return nil
}

// backgroundVoiceRun has a channel agent hand work off from one message, voice
// note or typed, and returns what reached the chat when the result came back,
// the thread's stored result card, and whether the turn that delivered it was
// told its reply would be heard.
func backgroundVoiceRun(t *testing.T, voice bool) (sends []voiceSend, card ChatMessage, toldHeard bool) {
	t.Helper()
	room := "room-" + UUIDv4()
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "find the venue"}}}},
		FakeTurn{Content: "The venue is the old mill.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You find venues."}); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "sms", AgentID: df.agent.ID})
	registerChannelAgentRunner(df.app)
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })
	prevRunner, prevLink := TaskRunnerFunc, ConversationLinkFunc
	df.app.installTaskRunner()
	t.Cleanup(func() { TaskRunnerFunc, ConversationLinkFunc = prevRunner, prevLink })

	var mu sync.Mutex
	var got []voiceSend
	prevThreads, had := ActiveChannelThreads()
	RegisterChannelThreads(voiceThreads{&mu, &got})
	t.Cleanup(func() {
		if had {
			RegisterChannelThreads(prevThreads)
		} else {
			RegisterChannelThreads(nil)
		}
	})
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("find the venue")) }))
	t.Cleanup(stt.Close)
	tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 32)...))
	}))
	t.Cleanup(tts.Close)
	prevSTT, prevTTS := GetTranscribeConfig(), media.GetSpeechConfig()
	SetTranscribeConfig(TranscribeConfig{Endpoint: stt.URL, Enabled: true})
	media.SetSpeechConfig(media.SpeechConfig{Endpoint: tts.URL, Enabled: true})
	t.Cleanup(func() {
		SetTranscribeConfig(prevSTT)
		media.SetSpeechConfig(prevTTS)
	})

	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, SessionID: room, ChatID: "chat-1", SenderName: "Alice", FromOwner: true}
	if voice {
		in.Audios = []string{base64.StdEncoding.EncodeToString([]byte("clip"))}
	} else {
		in.Text = "find the venue"
	}
	if _, err := RunChannelAgent(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	result := func() (ChatMessage, bool) {
		for _, m := range df.thread(room).Messages {
			if m.Role == "assistant" && m.ReportFrom != "" {
				return m, true
			}
		}
		return ChatMessage{}, false
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, ok := result(); ok {
			card = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background result never came back to the conversation")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			if m.Role == "user" && strings.Contains(m.Content, "BACKGROUND TASK FINISHED") && strings.Contains(m.Content, spokenReplyNote) {
				toldHeard = true
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]voiceSend(nil), got...), card, toldHeard
}

// Asked by voice note, a background result comes back the same way: written
// to be heard, sent to the chat that asked as a voice note, and marked spoken
// in the thread. Typed, it comes back as text.
func TestABackgroundResultIsAnsweredTheWayItWasAsked(t *testing.T) {
	sends, card, heard := backgroundVoiceRun(t, true)
	if !heard {
		t.Error("the turn delivering a spoken request's result was not told it will be heard")
	}
	if len(sends) != 1 || sends[0].chatID != "chat-1" || sends[0].voice == "" || sends[0].text == "" {
		t.Errorf("the result should reach the chat that asked as a voice note with its words: %+v", sends)
	}
	if !card.Spoken {
		t.Error("the stored result should be marked spoken")
	}

	sends, card, heard = backgroundVoiceRun(t, false)
	if heard || card.Spoken {
		t.Errorf("a typed request's result is not for hearing: told=%v spoken=%v", heard, card.Spoken)
	}
	if len(sends) != 1 || sends[0].chatID != "chat-1" || sends[0].voice != "" {
		t.Errorf("a typed request's result goes back as text: %+v", sends)
	}
}

// A dictated web message with read-aloud on hands its background work the same
// mark a voice note does, and the turn delivering a spoken result passes it on
// with the chat it is for: a job that turn starts comes back the same way.
func TestTheSpokenMarkFollowsTheWork(t *testing.T) {
	app := &OrchestrateApp{}
	if !(&chatTurn{app: app, spokenReply: true}).newToolSession().SpokenReply {
		t.Error("a dictated turn's work lost the spoken mark")
	}
	if (&chatTurn{app: app}).newToolSession().SpokenReply {
		t.Error("a typed turn's work was marked spoken")
	}
	prevTTS := media.GetSpeechConfig()
	media.SetSpeechConfig(media.SpeechConfig{Endpoint: "http://speech.invalid", Enabled: true})
	t.Cleanup(func() { media.SetSpeechConfig(prevTTS) })
	fr := &fireRun{app: app, p: orchUpdatePayload{SessionID: "room-1", Prompt: taskWakeMarker + " done",
		SpokenReply: true, ChannelChatID: "chat-1", ChannelHandle: "+15550100"}}
	fr.buildSession()
	if s := fr.subSess; !s.SpokenReply || s.ChannelChatID != "chat-1" || s.ChannelHandle != "+15550100" {
		t.Errorf("the result's turn should pass on how and where it was asked: %+v", s)
	}
}
