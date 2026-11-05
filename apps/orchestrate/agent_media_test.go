package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/filenotes"
)

// What an agent attaches comes back to the agent that ran it and goes out with
// that one's reply. Agents passed each other text and nothing else: a song a
// music agent made reached its caller as a sentence saying it was made, and on
// a channel turn it reached nobody.

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
	waitBackgroundJobs(t, room)

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

// What a run an agent started created comes back to that agent: copied into
// its folder when the run worked in another (a different agent's own), left
// where it is when the two share one (a sub-agent and its parent).

func TestFilesARunCreatedComeBackToTheAgentThatRanIt(t *testing.T) {
	theirs, mine := workFolders(t)
	os.WriteFile(filepath.Join(theirs, "song.mp3"), []byte("mp3"), 0600)
	os.WriteFile(filepath.Join(theirs, "notes.txt"), []byte("lyrics"), 0600)
	os.WriteFile(filepath.Join(mine, "notes.txt"), []byte("my own notes"), 0600)
	run := &ToolSession{WorkspaceDir: theirs}
	// What its tools created this run, one of them since removed (attached
	// with cleanup).
	run.AddStagedFiles([]string{"song.mp3", "notes.txt", "gone.png"})
	caller := &ToolSession{WorkspaceDir: mine}

	note := handBack("Music", run, caller)
	if b, err := os.ReadFile(filepath.Join(mine, "song.mp3")); err != nil || string(b) != "mp3" {
		t.Fatalf("the song is not in the caller's folder: %v", err)
	}
	// A name the caller already has is kept, and the copy takes the next one.
	if b, _ := os.ReadFile(filepath.Join(mine, "notes.txt")); string(b) != "my own notes" {
		t.Error("the caller's own file was overwritten")
	}
	if b, err := os.ReadFile(filepath.Join(mine, "notes-2.txt")); err != nil || string(b) != "lyrics" {
		t.Errorf("the second notes.txt was not copied under a free name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mine, "gone.png")); err == nil {
		t.Error("a file the run removed was conjured")
	}
	// The renamed copy is named as the new one, and the caller's own file
	// under the old name as older: the other agent's reply calls it by the
	// name it saved, and going by that name attaches the wrong file.
	for _, want := range []string{"Music created", "song.mp3", "notes-2.txt", "attach any you mean to send",
		"notes.txt is notes-2.txt in your folder", "an older one"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q: %s", want, note)
		}
	}
	// They count as this turn's, so attaching one is not warned about as old.
	staged := strings.Join(caller.StagedFiles(), ",")
	if !strings.Contains(staged, "song.mp3") || !strings.Contains(staged, "notes-2.txt") {
		t.Errorf("the copies are not this turn's files: %s", staged)
	}

	// A sub-agent works in its parent's folder: nothing to copy.
	shared := &ToolSession{WorkspaceDir: mine}
	shared.AddStagedFiles([]string{"song.mp3"})
	if note := handBack("Sub", shared, &ToolSession{WorkspaceDir: mine}); note != "" {
		t.Errorf("a run in the caller's own folder reported a copy: %s", note)
	}
}

// A delegation hands back through a collecting session with no folder of its
// own, so the files ride it by absolute path.
func TestFilesComeBackThroughADelegationsCollector(t *testing.T) {
	theirs, mine := workFolders(t)
	os.WriteFile(filepath.Join(theirs, "report.md"), []byte("findings"), 0600)
	run := &ToolSession{WorkspaceDir: theirs}
	run.AddStagedFiles([]string{"report.md"})
	sink := &ToolSession{}
	returnMadeFiles(run, sink)
	if got := madeFiles(sink); len(got) != 1 || got[0] != filepath.Join(theirs, "report.md") {
		t.Fatalf("the collector holds %v", got)
	}
	if note := handBack("Researcher", sink, &ToolSession{WorkspaceDir: mine}); !strings.Contains(note, "report.md") {
		t.Errorf("note: %s", note)
	}
	if _, err := os.Stat(filepath.Join(mine, "report.md")); err != nil {
		t.Errorf("not copied: %v", err)
	}
}

func TestAHandBackTooLargeStaysWhereItIs(t *testing.T) {
	prev := handBackFileLimit
	handBackFileLimit = 4
	t.Cleanup(func() { handBackFileLimit = prev })
	theirs, mine := workFolders(t)
	os.WriteFile(filepath.Join(theirs, "film.mp4"), []byte("too large"), 0600)
	run := &ToolSession{WorkspaceDir: theirs}
	run.AddStagedFiles([]string{"film.mp4"})
	note := handBack("Video", run, &ToolSession{WorkspaceDir: mine})
	if _, err := os.Stat(filepath.Join(mine, "film.mp4")); err == nil {
		t.Error("copied past the limit")
	}
	if !strings.Contains(note, "too large to copy") || !strings.Contains(note, "film.mp4") {
		t.Errorf("the note should say what stayed behind: %s", note)
	}
}

// A run reports what its tools created, so the agent that ran it in the
// background can be handed them when the result arrives.
func TestARunReportsTheFilesItCreated(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "workspace", Args: map[string]any{"action": "write", "path": "song.txt", "content": "la la"}}}},
		FakeTurn{Content: "Wrote it."},
	)
	res := df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Write a song."})
	if len(res.Made) != 1 || filepath.Base(res.Made[0]) != "song.txt" {
		t.Fatalf("made %v, want the file its tool wrote (tool calls %+v)", res.Made, res.ToolCalls)
	}
	if b, err := os.ReadFile(res.Made[0]); err != nil || string(b) != "la la" {
		t.Errorf("the path does not hold what was written: %v", err)
	}
}

// The synchronous dispatch (delegate, a Source) hands what its tools created to
// the collecting session on its context, as it does what they attached.
func TestASyncDispatchHandsItsFilesToTheCollector(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "workspace", Args: map[string]any{"action": "write", "path": "report.md", "content": "findings"}}}},
		FakeTurn{Content: "Done."},
	)
	sink := &ToolSession{}
	if _, err := df.app.runAgentSyncConfirm(withMediaReturn(t.Context(), sink), "u", "u", df.agent.ID, "Write the report.", func(string, string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got := madeFiles(sink); len(got) != 1 || filepath.Base(got[0]) != "report.md" {
		t.Errorf("the collector holds %v, want the report the run wrote", got)
	}
}

// Any file can be handed to an agent: pictures, audio and video as its media,
// anything else copied into its folder under received/, which the note names.
func TestAnyFileCanBeHandedToAnotherAgent(t *testing.T) {
	mine, theirs := workFolders(t)
	os.WriteFile(filepath.Join(mine, "notes.md"), []byte("the brief"), 0600)
	os.WriteFile(filepath.Join(mine, "data.csv"), []byte("a,b"), 0600)
	caller := &ToolSession{WorkspaceDir: mine}
	caller.RegisterInboundMedia("image", []byte("not really a png"), "someone")

	items, note, err := handOut(caller, "Wren", []string{"notes.md", "media#1", "data.csv"})
	if err != nil {
		t.Fatal(err)
	}
	// The picture is media#1 though a file came before it: files take no id.
	for _, want := range []string{"received/notes.md (a file, which Wren called notes.md", "media#1 (an image, which Wren called media#1)", "received/data.csv"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q: %s", want, note)
		}
	}
	run := &ToolSession{WorkspaceDir: theirs}
	receiveHanded(run, items)
	if b, err := os.ReadFile(filepath.Join(theirs, "received", "notes.md")); err != nil || string(b) != "the brief" {
		t.Errorf("the file is not where the note says: %v", err)
	}
	if b, _, ok := run.ResolveInboundMedia("media#1"); !ok || b == "" {
		t.Error("the picture is not the run's media#1")
	}
	// Sent again, the newer copy replaces the older.
	os.WriteFile(filepath.Join(mine, "notes.md"), []byte("the brief, revised"), 0600)
	items, _, _ = handOut(caller, "Wren", []string{"notes.md"})
	receiveHanded(run, items)
	if b, _ := os.ReadFile(filepath.Join(theirs, "received", "notes.md")); string(b) != "the brief, revised" {
		t.Errorf("the second hand-over did not replace the first: %q", b)
	}

	// A picture in the folder still goes over as media, not as a file.
	os.WriteFile(filepath.Join(mine, "photo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0600)
	if items, note, err := handOut(caller, "Wren", []string{"photo.png"}); err != nil || len(items) != 1 || items[0].kind != "image" || !strings.Contains(note, "media#1") {
		t.Errorf("a picture was not handed as media: %+v %q %v", items, note, err)
	}

	// Refused: a name that is nothing, a folder, a path out of the folder, and
	// more than a hand-over copies.
	os.Mkdir(filepath.Join(mine, "sub"), 0700)
	for _, bad := range []string{"nothing.txt", "sub", "../escape.txt"} {
		if _, _, err := handOut(caller, "Wren", []string{bad}); err == nil || !strings.Contains(err.Error(), "a file in your working folder") {
			t.Errorf("%q was handed over: %v", bad, err)
		}
	}
	prev := handBackFileLimit
	handBackFileLimit = 4
	t.Cleanup(func() { handBackFileLimit = prev })
	if _, _, err := handOut(caller, "Wren", []string{"notes.md"}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a hand-over past the limit was accepted: %v", err)
	}
}

// Through a delegation: the run's folder is known only once its session is
// built, and the file has to land in it.
func TestADelegationReceivesAHandedFile(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t, FakeTurn{Content: "Read it."})
	mine := filepath.Join(WorkspacesDir(), "caller")
	os.MkdirAll(mine, 0700)
	os.WriteFile(filepath.Join(mine, "brief.txt"), []byte("do the thing"), 0600)
	items, _, err := handOut(&ToolSession{WorkspaceDir: mine}, "Wren", []string{"brief.txt"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withHandedMedia(t.Context(), items)
	if _, err := df.app.runAgentSyncConfirm(ctx, "u", "u", df.agent.ID, "Read the brief.", func(string, string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	home := agentWorkspace(df.udb, "u", df.agent)
	if b, err := os.ReadFile(filepath.Join(home, "received", "brief.txt")); err != nil || string(b) != "do the thing" {
		t.Errorf("the delegated agent did not receive the file in its folder %s: %v", home, err)
	}
}

// A file handed back from the agent that made it keeps what it is, under its
// copy's name, and the note telling the caller about it says which is which:
// the caller already had a generated_music.mp3, so the copy is -2, and a list
// of generated names alone does not say that -2 is the new song.
func TestAHandedBackFileKeepsWhatItIs(t *testing.T) {
	mine, theirs := workFolders(t)
	os.WriteFile(filepath.Join(mine, "generated_music.mp3"), []byte("old"), 0o644)
	src := filepath.Join(theirs, "generated_music.mp3")
	os.WriteFile(src, []byte("new"), 0o644)
	filenotes.Record(src, filenotes.Note{Title: "Gas Prices Are Too High", By: "generate_music", Request: "make it country rock"})
	plain := filepath.Join(theirs, "notes.txt")
	os.WriteFile(plain, []byte("lyrics"), 0o644)

	to := &ToolSession{Username: "u", WorkspaceDir: mine}
	msg := handBackFiles("Lyric & Music Generator", []string{src, plain}, to)
	if n, ok := filenotes.Get(filepath.Join(mine, "generated_music-2.mp3")); !ok || n.Title != "Gas Prices Are Too High" || n.From != "Lyric & Music Generator" {
		t.Errorf("the copy keeps its note, marked as handed back: %+v", n)
	}
	if !strings.Contains(msg, `generated_music-2.mp3 ("Gas Prices Are Too High", made by generate_music (from Lyric & Music Generator)`) {
		t.Errorf("the hand-back says what the copy is:\n%s", msg)
	}
	if n, ok := filenotes.Get(filepath.Join(mine, "notes.txt")); !ok || n.From != "Lyric & Music Generator" {
		t.Errorf("a file with no note is noted as handed back: %+v", n)
	}
}

// The history keeps what each sent file was, as its attach said: a later "send
// that song again" resolves to the right one.
func TestTheHistorySaysWhatASentFileWas(t *testing.T) {
	m := ChatMessage{Role: "assistant", Content: "Sent.", Files: []deliveredFile{{Name: "generated_music-2.mp3", Kind: "audio"}},
		ToolCalls: []PersistedToolCall{{Name: "workspace", Args: map[string]any{"action": "attach", "path": "generated_music-2.mp3"},
			Result: `Attached "generated_music-2.mp3" (audio/mpeg, 3.7 MB) via video channel. It is "Gas Prices Are Too High", made by generate_music Oct 28 20:06, for "make it country rock". (cleaned up from workspace)`}}}
	note := deliveredNote(m)
	if !strings.Contains(note, `What they were: generated_music-2.mp3 is "Gas Prices Are Too High", made by generate_music Oct 28 20:06, for "make it country rock".`) {
		t.Errorf("the delivery note says what the song was:\n%s", note)
	}
	if strings.Contains(note, "cleaned up") {
		t.Errorf("the attach's own suffixes stay out of it:\n%s", note)
	}
	if attachedDescription(`Attached "x.png" (image/png, 1 KB) via image channel.`) != "" {
		t.Error("an attach with no note describes nothing")
	}
}

// workFolders is two working folders under a workspaces root of their own,
// as every real one is: a file is handed on only from under that root.
func workFolders(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	SetWorkspacesDir(root)
	t.Cleanup(func() { SetWorkspacesDir("") })
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	os.MkdirAll(a, 0700)
	os.MkdirAll(b, 0700)
	return a, b
}

// A link a run left in its folder is not handed on: the server, which makes
// the copy and is not sandboxed, would have read wherever it pointed.
func TestALinkARunLeftIsNotHandedOn(t *testing.T) {
	theirs, mine := workFolders(t)
	secret := filepath.Join(t.TempDir(), "server.db")
	os.WriteFile(secret, []byte("private"), 0600)
	link := filepath.Join(theirs, "report.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("no symlinks here")
	}
	os.Symlink(filepath.Dir(secret), filepath.Join(theirs, "out"))
	viaDir := filepath.Join(theirs, "out", "server.db")

	to := &ToolSession{WorkspaceDir: mine}
	handBackFiles("Scout", []string{link, viaDir}, to)
	if got := to.StagedFiles(); len(got) != 0 {
		t.Errorf("a hand-back copied through a link: %v", got)
	}
	if got := handToScript("Scout", stagedRun(link, viaDir), filepath.Join(t.TempDir(), "run")); len(got) != 0 {
		t.Errorf("an app script was handed a link's target: %v", got)
	}
	if err := receiveHandedFile(to, link); err == nil {
		t.Error("a handed link was received")
	}
	if err := receiveHandedFile(to, viaDir); err == nil {
		t.Error("a file through a linked folder was received")
	}
	for _, name := range []string{"report.md", "server.db"} {
		if b, _ := os.ReadFile(filepath.Join(mine, "received", name)); len(b) != 0 {
			t.Errorf("the target reached the receiving folder as %s: %q", name, b)
		}
	}
}

// stagedRun is a run that reports having made paths.
func stagedRun(paths ...string) *ToolSession {
	s := &ToolSession{}
	s.AddStagedFiles(paths)
	return s
}

// A file handed from one run to another is read and written inside the
// workspaces as the copy is made: a folder on either path swapped for a link
// (by a script still running there) leads nowhere.
func TestAHandedFileIsCopiedInsideTheWorkspaces(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "workspaces"), filepath.Join(base, "server-data")
	SetWorkspacesDir(root)
	t.Cleanup(func() { SetWorkspacesDir("") })
	sender, receiver := filepath.Join(root, "alice", "sender"), filepath.Join(root, "alice", "receiver")
	os.MkdirAll(filepath.Join(sender, "out"), 0o700)
	os.MkdirAll(receiver, 0o700)
	os.MkdirAll(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private"), 0o600)
	os.WriteFile(filepath.Join(sender, "out", "song.mp3"), []byte("la"), 0o600)

	// The receiver's received/ folder swapped for a link out.
	if err := os.Symlink(outside, filepath.Join(receiver, handedFilesDir)); err != nil {
		t.Skip("no symlinks here")
	}
	sess := &ToolSession{WorkspaceDir: receiver}
	if err := receiveHandedFile(sess, filepath.Join(sender, "out", "song.mp3")); err == nil {
		t.Error("the copy went through a folder link")
	}
	if _, err := os.Stat(filepath.Join(outside, "song.mp3")); err == nil {
		t.Error("the handed file landed outside the workspaces")
	}

	// The sender's folder swapped for a link out, after the path was checked.
	os.Remove(filepath.Join(receiver, handedFilesDir))
	os.RemoveAll(filepath.Join(sender, "out"))
	os.Symlink(outside, filepath.Join(sender, "out"))
	if _, err := openHandedSource(filepath.Join(sender, "out", "secret.txt")); err == nil {
		t.Error("read the server's file through a folder link")
	}

	// An ordinary hand-off.
	os.Remove(filepath.Join(sender, "out"))
	os.MkdirAll(filepath.Join(sender, "out"), 0o700)
	os.WriteFile(filepath.Join(sender, "out", "song.mp3"), []byte("la"), 0o600)
	if err := receiveHandedFile(sess, filepath.Join(sender, "out", "song.mp3")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(receiver, handedFilesDir, "song.mp3")); string(b) != "la" {
		t.Errorf("the handed copy: %q", b)
	}
}

// The race the copy is held against: the receiver's received/ folder swapped
// for a link out and back as fast as a script can, while files are handed in.
func TestAHandedFileNeverEscapesARacingSwap(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "workspaces"), filepath.Join(base, "server-data")
	SetWorkspacesDir(root)
	t.Cleanup(func() { SetWorkspacesDir("") })
	sender, receiver := filepath.Join(root, "alice", "sender"), filepath.Join(root, "alice", "receiver")
	os.MkdirAll(sender, 0o700)
	os.MkdirAll(filepath.Join(receiver, handedFilesDir), 0o700)
	os.MkdirAll(outside, 0o700)
	src := filepath.Join(sender, "song.mp3")
	os.WriteFile(src, []byte("la"), 0o600)
	received := filepath.Join(receiver, handedFilesDir)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.RemoveAll(received)
			os.Symlink(outside, received)
			os.Remove(received)
			os.Mkdir(received, 0o700)
		}
	}()
	sess := &ToolSession{WorkspaceDir: receiver}
	for i := 0; i < 3000; i++ {
		receiveHandedFile(sess, src)
	}
	close(stop)
	<-done
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("a handed file landed outside the workspaces: %v", entries)
	}
}

// A hand-off's source is read within its owner's part of the workspaces, so a
// folder swapped for a link to another person's workspace leads nowhere, as
// one to the server's data does.
func TestAHandedFileIsReadWithinItsOwnersWorkspaces(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspaces")
	SetWorkspacesDir(root)
	t.Cleanup(func() { SetWorkspacesDir("") })
	j := func(p ...string) string { return filepath.Join(append([]string{root}, p...)...) }
	for _, c := range []struct {
		src, want string
		ok        bool
	}{
		{j("alice", "notes.txt"), j("alice"), true},
		{j("alice", "_ws", "w1", "out", "a.txt"), j("alice"), true},
		{j(".agents", "alice", "helper", "a.txt"), j(".agents", "alice"), true},
		{j("_flows", "run1", "a.txt"), j("_flows", "run1"), true},
		{j("app:servitor:1", "a.txt"), j("app:servitor:1"), true},
		{j("alice"), "", false},
		{j(".agents", "alice"), "", false},
		{filepath.Join(base, "elsewhere", "a.txt"), "", false},
	} {
		got, ok := handSourceRoot(c.src)
		if ok != c.ok || got != c.want {
			t.Errorf("handSourceRoot(%s) = %q %v, want %q %v", c.src, got, ok, c.want, c.ok)
		}
	}

	alice, bob := j(".agents", "alice", "helper"), j(".agents", "bob", "helper")
	os.MkdirAll(alice, 0o700)
	os.MkdirAll(filepath.Join(bob, "private"), 0o700)
	os.WriteFile(filepath.Join(bob, "private", "diary.txt"), []byte("bob's"), 0o600)
	if err := os.Symlink(filepath.Join(bob, "private"), filepath.Join(alice, "out")); err != nil {
		t.Skip("no symlinks here")
	}
	if f, err := openHandedSource(filepath.Join(alice, "out", "diary.txt")); err == nil {
		f.Close()
		t.Error("read another person's file through a folder link")
	}
	// Within alice's own part, a (relative) link leads only to alice's files.
	// An absolute one is refused whatever it names: os.Root takes none.
	os.MkdirAll(j(".agents", "alice", "other"), 0o700)
	os.WriteFile(j(".agents", "alice", "other", "song.mp3"), []byte("la"), 0o600)
	os.Remove(filepath.Join(alice, "out"))
	os.Symlink(filepath.Join("..", "other"), filepath.Join(alice, "out"))
	f, err := openHandedSource(filepath.Join(alice, "out", "song.mp3"))
	if err != nil {
		t.Errorf("a link within the owner's own workspaces: %v", err)
	} else {
		f.Close()
	}
}
