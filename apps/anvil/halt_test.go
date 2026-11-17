package anvil

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A change reverted while the turn that made it is still running halts that
// turn: it is told, every tool that changes something refuses (reading
// still works), and the person's next message lifts it. A turn's first
// message carries no session id, so its tools learn it from the turn, and
// the change records the session that made it.
func TestARevertMidTurnHaltsTheTurn(t *testing.T) {
	T, serve := bundleRoutes(t)
	udb := UserDB(T.DB, "root")
	root := t.TempDir()
	p := Project{ID: "h1", Name: "H", Kind: KindRoot, Root: root, Owner: "root"}
	T.saveProject(udb, p)
	mustWrite(t, filepath.Join(root, "a.txt"), "one\n")
	mustWrite(t, filepath.Join(root, "b.txt"), "bee\n")

	const sid = "halt-test-session"
	q := RegisterInjectionQueue(sid, "root", "anvil")
	t.Cleanup(func() { ReleaseInjectionQueue(sid); liftHalt(sid) })
	ctx := (&ToolSession{ChatSessionID: sid}).ContextWithSession(context.Background())
	c := &toolCtx{udb: udb, user: "root", isAdmin: true, project: p, base: root}
	tools := map[string]AgentToolDef{}
	for _, td := range c.codeTools() {
		tools[td.Tool.Name] = td
	}
	call := func(name string, args map[string]any) (string, error) {
		return tools[name].Handler(ctx, args)
	}

	if _, err := call("edit_file", map[string]any{"path": "a.txt", "old_text": "one", "new_text": "two"}); err != nil {
		t.Fatal(err)
	}
	edits := listEdits(udb, p.ID, sid)
	if len(edits) != 1 || edits[0].SessionID != sid {
		t.Fatalf("the change does not name the turn's session: %+v", edits)
	}
	if w := serve("root", "POST", fmt.Sprintf("/api/edit/%s/revert", edits[0].ID), nil, ""); w.Code != 200 {
		t.Fatalf("revert: %d %s", w.Code, w.Body.String())
	}
	if notes := q.Pending(); len(notes) != 1 || !strings.Contains(notes[0].Text, "a.txt was reverted") {
		t.Errorf("the running turn was not told: %+v", notes)
	}
	for name, args := range map[string]map[string]any{
		"edit_file":   {"path": "b.txt", "old_text": "bee", "new_text": "wasp"},
		"create_file": {"path": "c.txt", "content": "new\n"},
		"delete_file": {"path": "b.txt"},
		"run":         {"command": "true"},
	} {
		if _, err := call(name, args); err == nil || !strings.Contains(err.Error(), "Change nothing more this turn") {
			t.Errorf("%s after the revert: %v", name, err)
		}
	}
	if got := readFile(t, filepath.Join(root, "b.txt")); got != "bee\n" {
		t.Errorf("b.txt was changed during the halt: %q", got)
	}
	if out, err := call("read_file", map[string]any{"path": "b.txt"}); err != nil || !strings.Contains(out, "bee") {
		t.Errorf("reading stopped too: %v", err)
	}

	// A note typed while the turn runs is the person's say, and lifts it.
	serve("root", "POST", "/api/inject", []byte(`{"id":"`+sid+`","text":"keep the old name, carry on"}`), "application/json")
	if _, err := call("edit_file", map[string]any{"path": "b.txt", "old_text": "bee", "new_text": "wasp"}); err != nil {
		t.Errorf("after the person's note: %v", err)
	}
}
