package orchestrate

// What a run an agent started created comes back to that agent: copied into
// its folder when the run worked in another (a different agent's own), left
// where it is when the two share one (a sub-agent and its parent).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestFilesARunCreatedComeBackToTheAgentThatRanIt(t *testing.T) {
	theirs, mine := t.TempDir(), t.TempDir()
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
	for _, want := range []string{"Music created", "song.mp3", "notes-2.txt", "attach any you mean to send"} {
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
	theirs, mine := t.TempDir(), t.TempDir()
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
	theirs, mine := t.TempDir(), t.TempDir()
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
	mine, theirs := t.TempDir(), t.TempDir()
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
	df := newDispatchFixture(t, FakeTurn{Content: "Read it."})
	mine := t.TempDir()
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
