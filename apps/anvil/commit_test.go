package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// turnTool is the turn's tool called name, or nil.
func turnTool(c *toolCtx, name string) *AgentToolDef {
	for _, d := range c.codeTools() {
		if d.Tool.Name == name {
			return &d
		}
	}
	return nil
}

// The assistant commits a checkout's work, as the person, with its message,
// and the commit is a card in the conversation; Undo takes it back while it
// is still the last commit and leaves its changes uncommitted. A reader has
// no commit, and nothing to commit is said, not committed.
func TestTheAssistantCommitsAndTheCommitCanBeUndone(t *testing.T) {
	T, serve := appProjectWorld(t)
	udb := UserDB(T.DB, "alice")
	p := Project{ID: UUIDv4(), Name: "tool", Kind: KindClone, Owner: "alice", Source: sourceNew}
	base, _ := p.baseDir()
	os.MkdirAll(base, 0o700)
	runGit(base, "init", "-q")
	os.WriteFile(filepath.Join(base, "a.txt"), []byte("one\n"), 0o644)
	commitCheckout(base, "alice", "Start")
	T.saveProject(udb, p)

	var cards []agenthost.Block
	c := &toolCtx{user: "alice", udb: udb, project: p, base: base, sessionID: "s1",
		emitBlock: func(b agenthost.Block) { cards = append(cards, b) }}
	commit := turnTool(c, "commit")
	if commit == nil {
		t.Fatal("a checkout offers no commit")
	}
	if turnTool(&toolCtx{user: "alice", udb: udb, project: p, base: base, readOnly: true}, "commit") != nil {
		t.Error("a reader may commit")
	}
	if _, err := commit.Handler(context.Background(), map[string]any{"message": "Nothing yet"}); err == nil {
		t.Error("a commit with nothing to commit went through")
	}

	os.WriteFile(filepath.Join(base, "a.txt"), []byte("two\n"), 0o644)
	msg := "Change one to two\n\nThe count starts at two now."
	out, err := commit.Handler(context.Background(), map[string]any{"message": msg})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if log, _ := runGit(base, "log", "-1", "--format=%an|%B"); !strings.HasPrefix(log, "alice|Change one to two\n\nThe count starts at two now.") {
		t.Errorf("the commit: %q", log)
	}
	if !strings.Contains(out, "Change one to two") || len(cards) != 1 || cards[0].Type != commitBlock || cards[0].Data["body"] != "The count starts at two now." {
		t.Fatalf("the answer %q, the cards %+v", out, cards)
	}
	hash := cards[0].Data["hash"]

	// A later commit makes it no longer the last: its Undo is refused.
	os.WriteFile(filepath.Join(base, "b.txt"), []byte("b\n"), 0o644)
	commit.Handler(context.Background(), map[string]any{"message": "Add b"})
	if w := serve("alice", "POST", "/api/project/"+p.ID+"/commit/undo", `{"hash":"`+hash+`"}`); w.Code != 409 {
		t.Errorf("undo of a commit with one after it: %d %s", w.Code, w.Body.String())
	}
	last := cards[1].Data["hash"]
	if w := serve("bob", "POST", "/api/project/"+p.ID+"/commit/undo", `{"hash":"`+last+`"}`); w.Code == 200 {
		t.Error("somebody else undid alice's commit")
	}
	if w := serve("alice", "POST", "/api/project/"+p.ID+"/commit/undo", `{"hash":"`+last+`"}`); w.Code != 200 {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if head, _ := runGit(base, "rev-parse", "HEAD"); strings.TrimSpace(head) != hash {
		t.Errorf("after the undo the last commit is %s, want %s", head, hash)
	}
	if st, _ := runGit(base, "status", "--porcelain"); !strings.Contains(st, "b.txt") {
		t.Errorf("the undone commit's change is gone: %q", st)
	}

	ctx := T.projectContext(p, base)
	if !strings.Contains(ctx, "with commit once it is done") || strings.Contains(ctx, "you do not commit") {
		t.Errorf("the assistant is not told it commits:\n%s", ctx)
	}
}

// An app project's commits are its installs: the assistant writes the
// message with install_message, and Install commits with it and puts it in
// the app's history, then it is spent. An app project has no commit.
func TestAnAppsInstallCommitsWithTheAssistantsMessage(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	udb := UserDB(T.DB, "alice")
	var p Project
	udb.Get(projectTable, id, &p)
	base, _ := p.baseDir()
	c := &toolCtx{user: "alice", udb: udb, project: p, base: base, sessionID: "s1"}
	if turnTool(c, "commit") != nil {
		t.Error("an app project offers commit")
	}
	note := turnTool(c, "install_message")
	if note == nil {
		t.Fatal("an app project offers no install_message")
	}
	os.WriteFile(filepath.Join(base, "data", "now.py"), []byte("print('{\"t\": 3}')"), 0o644)
	msg := "Report the temperature as three\n\nThe sensor reads three now."
	if _, err := note.Handler(context.Background(), map[string]any{"message": msg}); err != nil {
		t.Fatal(err)
	}
	if w := serve("alice", "POST", "/api/project/"+id+"/install", "{}"); w.Code != 200 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if log, _ := runGit(base, "log", "-1", "--format=%B"); strings.TrimSpace(log) != msg {
		t.Errorf("the install's commit: %q", log)
	}
	if live, _ := LoadAppSpec("alice", "wx"); live.ChangeNote != msg {
		t.Errorf("the app's history: %q", live.ChangeNote)
	}
	if installMessage(udb, id) != "" {
		t.Error("the message outlived the install it was for")
	}
	if !strings.Contains(T.projectContext(p, base), "install_message") {
		t.Error("the assistant is not told to write the install's message")
	}
}
