package orchestrate

// What an agent attaches comes back to the agent that ran it and goes out with
// that one's reply. Agents passed each other text and nothing else: a song a
// music agent made reached its caller as a sentence saying it was made, and on
// a channel turn it reached nobody.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestASongAnAgentMadeGoesOutWithItsCallersReply(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "make the song"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s1", Name: "workspace", Args: map[string]any{"action": "write", "path": "drain.mp3", "content": "ID3 drain"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s2", Name: "workspace", Args: map[string]any{"action": "attach", "path": "drain.mp3"}}}},
		FakeTurn{Content: "Here it is.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make songs."}); err != nil {
		t.Fatal(err)
	}
	res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "make me a song", MessageSender: "u", SenderIsOwner: true})

	if n := len(res.Videos) + len(res.Images); n != 1 {
		t.Fatalf("the channel reply should carry Scout's song: %d image(s), %d audio/video", len(res.Images), len(res.Videos))
	}
	told := false
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				if r.ID == "c1" && strings.Contains(r.Content, "[Scout attached ") && strings.Contains(r.Content, "goes out with your reply") {
					told = true
				}
			}
		}
	}
	if !told {
		t.Error("the caller was not told what Scout's run brought back")
	}
}

// By delegate, the run goes through a delegation, whose result was text alone.
func TestASongADelegateMadeComesBackToTheAgentThatAsked(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "delegate", Args: map[string]any{"agent": "Scout", "brief": "make the song"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s1", Name: "workspace", Args: map[string]any{"action": "write", "path": "drain.mp3", "content": "ID3 drain"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s2", Name: "workspace", Args: map[string]any{"action": "attach", "path": "drain.mp3"}}}},
		FakeTurn{Content: "Here it is.", Repeat: true})
	df.agent.Fleet = true
	if _, err := saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make songs."}); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	registerStandingRunner(df.app)
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "make me a song", MessageSender: "u", SenderIsOwner: true})

	if n := len(res.Videos) + len(res.Images); n != 1 {
		t.Errorf("the channel reply should carry the delegate's song: %d image(s), %d audio/video", len(res.Images), len(res.Videos))
	}
	told := false
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				if r.ID == "c1" && strings.Contains(r.Content, "[Scout attached ") {
					told = true
				}
			}
		}
	}
	if !told {
		t.Error("the agent that delegated was not told what came back")
	}
}

// An approved request's run brings its song back to the conversation that
// asked, with the result, rather than the result alone.
func TestAnApprovedRequestsSongReachesTheConversationThatAsked(t *testing.T) {
	// Its own thread: a finished task's wake coalesces per session id across
	// the process, so a shared id hands this wake to another test's window.
	room := "room-" + UUIDv4()
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "s1", Name: "workspace", Args: map[string]any{"action": "write", "path": "drain.mp3", "content": "ID3 drain"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s2", Name: "workspace", Args: map[string]any{"action": "attach", "path": "drain.mp3"}}}},
		FakeTurn{Content: "Here it is.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make songs."}); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	registerStandingRunner(df.app)
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	saveChatSession(df.udb, ChatSession{ID: room, AgentID: df.agent.ID, Title: "Room"})

	df.app.runApprovedDelegation(Authorization{Owner: "u", Agent: "Scout", Brief: "make the song", FromAgent: df.agent.ID, FromSession: room}, "Scout")

	s := df.thread(room)
	carried := false
	for _, m := range s.Messages {
		if m.Role == "assistant" && len(m.Files)+len(m.Attachments) > 0 {
			carried = true
		}
	}
	if !carried {
		t.Errorf("the woken reply should carry the song: %+v", s.Messages)
	}
}

// A channel agent with no tool list of its own can still deliver a file: the
// default pool leaves framework tools out, and workspace with them.
func TestAChannelAgentWithNoToolListCanDeliverAFile(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "workspace", Args: map[string]any{"action": "write", "path": "drain.mp3", "content": "ID3 drain"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "w2", Name: "workspace", Args: map[string]any{"action": "attach", "path": "drain.mp3"}}}},
		FakeTurn{Content: "Here it is.", Repeat: true})
	res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "send me the song", MessageSender: "u", SenderIsOwner: true})
	if n := len(res.Videos) + len(res.Images); n != 1 {
		t.Errorf("the reply should carry the file the agent attached: %d image(s), %d audio/video", len(res.Images), len(res.Videos))
	}
}

// A handed-off run (await false) carries its song home on the detached
// session the finished task delivers from.
func TestAHandedOffRunBringsItsSongHome(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "s1", Name: "workspace", Args: map[string]any{"action": "write", "path": "drain.mp3", "content": "ID3 drain"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "s2", Name: "workspace", Args: map[string]any{"action": "attach", "path": "drain.mp3"}}}},
		FakeTurn{Content: "Here it is.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make songs."}); err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{app: df.app, user: "u", udb: df.udb, agent: df.agent, ctx: t.Context()}
	d := &ToolSession{Username: "u", ChatSessionID: "web-1", DB: df.udb, Ctx: t.Context()}
	out, err := turn.agentsDispatchPolicy(true).Detached(map[string]any{"action": "run", "agent": "Scout", "message": "make the song", "await": false}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Videos)+len(d.Images) != 1 {
		t.Errorf("the detached session should carry the song home: %d image(s), %d audio/video", len(d.Images), len(d.Videos))
	}
	if !strings.Contains(out, "[Scout attached ") {
		t.Errorf("the result should say what came with it: %q", out)
	}
}

// The other way: an agent hands the agent it runs a photo someone sent it, and
// that one holds it as its own, told what it is.
func TestAnAgentHandsOnAPhotoItWasSent(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	photo := []byte("\x89PNG the dam at dusk")
	df := newDispatchFixture(t,
		FakeTurn{Content: "A dam at dusk."}, // the arriving photo's caption
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "make a cover from this", "attach": []any{"media#1"}}}}},
		FakeTurn{Content: "Got it.", Repeat: true})
	scout, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make covers."})
	if err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "make a cover from my photo", MessageSender: "u", SenderIsOwner: true, Images: [][]byte{photo}})

	told := false
	for i := 0; i < df.llm.Calls(); i++ {
		if strings.Contains(df.llm.Prompt(i), "handed you, with this request: media#1 (an image") {
			told = true
		}
	}
	if !told {
		t.Error("Scout should be told what it was handed")
	}
	held, ok := ResolveRecentImage(&ToolSession{Username: "u", AgentID: scout.ID}, "image#1")
	if !ok || string(held) != string(photo) {
		t.Errorf("Scout should hold the photo as its own picture: ok=%v %q", ok, held)
	}
}

// A name the caller holds nothing under is refused, so it hears the run would
// have gone out without it.
func TestHandingOverSomethingYouDoNotHaveIsRefused(t *testing.T) {
	if _, _, err := handOut(&ToolSession{Username: "u"}, "Clerk", []string{"media#3"}); err == nil || !strings.Contains(err.Error(), "media#3") {
		t.Errorf("an unknown attachment should be refused by name: %v", err)
	}
	items, note, err := handOut(&ToolSession{Username: "u"}, "Clerk", nil)
	if err != nil || items != nil || note != "" {
		t.Errorf("nothing to hand over hands nothing: %v %q %v", items, note, err)
	}
}

// By delegate, and handed off (await false), the photo arrives the same way.
func TestAPhotoHandedOnByDelegateOrHandoffArrives(t *testing.T) {
	for _, door := range []string{"delegate", "handoff"} {
		SetWorkspacesDir(t.TempDir())
		t.Cleanup(func() { SetWorkspacesDir("") })
		photo := []byte("\x89PNG the dam at dusk, " + door)
		df := newDispatchFixture(t, FakeTurn{Content: "Got it.", Repeat: true})
		scout, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make covers."})
		if err != nil {
			t.Fatal(err)
		}
		prevRoot := RootDB
		RootDB = df.app.DB
		registerStandingRunner(df.app)
		caller := &ToolSession{Username: "u", ChatSessionID: "web-1", DB: df.udb, Ctx: t.Context(), AgentID: df.agent.ID}
		caller.RegisterInboundMedia("image", photo, "")
		switch door {
		case "delegate":
			// With no turn behind it, delegate runs at once only on Allow.
			SetDelegationPolicy(RootDB, "u", df.agent.ID, scout.ID, PolicyAllow)
			out, _ := delegateToolDef(caller, "u", df.agent.ID).Handler(caller.ContextWithSession(t.Context()), map[string]any{"agent": "Scout", "brief": "make a cover", "attach": []any{"media#1"}})
			if !strings.HasPrefix(out, "Delegated") {
				t.Fatalf("delegate: %q", out)
			}
		case "handoff":
			turn := &chatTurn{app: df.app, user: "u", udb: df.udb, agent: df.agent, ctx: t.Context()}
			if _, err := turn.agentsDispatchPolicy(true).Detached(map[string]any{"action": "run", "agent": "Scout", "message": "make a cover", "attach": []any{"media#1"}, "await": false}, caller); err != nil {
				t.Fatal(err)
			}
		}
		RegisterStandingRunner(nil)
		RegisterStandingReporter(nil)
		RootDB = prevRoot
		held, ok := ResolveRecentImage(&ToolSession{Username: "u", AgentID: scout.ID}, "image#1")
		if !ok || string(held) != string(photo) {
			t.Errorf("%s: Scout should hold the photo: ok=%v %q", door, ok, held)
		}
	}
}

// What a run is handed is that run's, not also handed to a run it starts.
func TestHandedMediaIsTakenOnce(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	ctx := withHandedMedia(t.Context(), []handedItem{{kind: "image", raw: []byte("\x89PNG x"), from: "Clerk"}})
	sess := &ToolSession{Username: "u", AgentID: "a-scout"}
	after := takeHandedMedia(ctx, sess)
	if sess.InboundMediaCount() != 1 {
		t.Fatalf("the run should hold what it was handed: %d", sess.InboundMediaCount())
	}
	if handedMediaFrom(after) != nil {
		t.Error("what was handed to this run would be handed again to a run it starts")
	}
}
