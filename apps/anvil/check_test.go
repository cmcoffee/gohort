package anvil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// checkCtx is a turn on a project whose check passes once ok.txt exists,
// recording the cards it publishes.
func checkCtx(t *testing.T, p Project) (*toolCtx, *[]agenthost.Block) {
	t.Helper()
	base := t.TempDir()
	if p.ID == "" {
		p.ID = "p1"
	}
	if p.CheckCommand == "" {
		p.CheckCommand = "test -f ok.txt"
	}
	var mu sync.Mutex
	var cards []agenthost.Block
	c := &toolCtx{project: p, base: base, udb: memDB(t), sessionID: "s1",
		emitBlock: func(b agenthost.Block) { mu.Lock(); cards = append(cards, b); mu.Unlock() }}
	return c, &cards
}

func toolNamed(t *testing.T, c *toolCtx, name string) (AgentToolDef, bool) {
	t.Helper()
	for _, td := range c.codeTools() {
		if td.Tool.Name == name {
			return td, true
		}
	}
	return AgentToolDef{}, false
}

// After the agent's edits the turn may not end on a failing check: the reply
// goes back with what the check said. Once the check passes it ends, and a
// turn that changed nothing runs no check at all.
func TestTheTurnEndsOnlyOnAPassingCheckAfterEdits(t *testing.T) {
	c, cards := checkCtx(t, Project{})
	if note := c.finishCheck("done"); note != "" || len(*cards) != 0 {
		t.Fatalf("a turn that changed nothing ran the check: %q %d card(s)", note, len(*cards))
	}
	create, _ := toolNamed(t, c, "create_file")
	if _, err := create.Handler(context.Background(), map[string]any{"path": "a.go", "content": "package a\n"}); err != nil {
		t.Fatal(err)
	}
	note := c.finishCheck("done")
	if !strings.Contains(note, "test -f ok.txt") || !strings.Contains(note, "check tool") {
		t.Fatalf("a failing check let the turn end, or did not say what failed: %q", note)
	}
	if err := os.WriteFile(filepath.Join(c.base, "ok.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if note := c.finishCheck("done"); note != "" {
		t.Fatalf("a passing check held the turn: %q", note)
	}
	if c.edited() {
		t.Error("a passing check left the tree marked as changed")
	}
	runs := 0
	for _, b := range *cards {
		if b.Type == checkBlock && b.Data["state"] == "running" {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("the check ran %d time(s), want 2 (once failing, once passing)", runs)
	}
}

// A project that asks for its check only on request is not checked as the
// turn ends; the agent still has the check tool.
func TestAManualCheckRunsOnlyWhenAsked(t *testing.T) {
	c, _ := checkCtx(t, Project{CheckManual: true})
	c.markEdited()
	if note := c.finishCheck("done"); note != "" {
		t.Fatalf("a manual check ran as the turn ended: %q", note)
	}
	tool, ok := toolNamed(t, c, "check")
	if !ok {
		t.Fatal("no check tool")
	}
	out, err := tool.Handler(context.Background(), map[string]any{})
	if err != nil || !strings.Contains(out, "The check failed") {
		t.Fatalf("%q %v", out, err)
	}
}

// No check without a command, and none for someone who may only read the
// project.
func TestThereIsNoCheckWithoutACommandOrForAReader(t *testing.T) {
	c, _ := checkCtx(t, Project{})
	c.project.CheckCommand = ""
	c.markEdited()
	if _, ok := toolNamed(t, c, "check"); ok || c.finishCheck("done") != "" {
		t.Error("a project with no check command was checked")
	}
	r, _ := checkCtx(t, Project{})
	r.readOnly = true
	r.markEdited()
	if _, ok := toolNamed(t, r, "check"); ok || r.finishCheck("done") != "" {
		t.Error("a reader's turn was checked")
	}
}

// A run's card says it is running, then how it went, under the same id, so the
// page updates one card rather than stacking two.
func TestACheckRunShowsRunningThenItsOutcome(t *testing.T) {
	c, cards := checkCtx(t, Project{CheckCommand: "echo building; exit 3"})
	c.runCheck(context.Background())
	if len(*cards) != 2 {
		t.Fatalf("%d cards", len(*cards))
	}
	first, last := (*cards)[0], (*cards)[1]
	if first.ID != last.ID || first.Data["state"] != "running" || last.Data["state"] != "failed" || !strings.Contains(last.Data["output"], "building") {
		t.Errorf("cards %+v / %+v", first, last)
	}
}

// The check and the run tool use the project's own package cache, outside
// the checkout, and nothing else's.
func TestTheCheckUsesTheProjectsOwnCache(t *testing.T) {
	ws := t.TempDir()
	prev := WorkspacesDir()
	SetWorkspacesDir(ws)
	t.Cleanup(func() {
		SetWorkspacesDir(prev)
		removeAllWritable(ws)
	})
	c, _ := checkCtx(t, Project{ID: "proj-a", CheckCommand: `echo "cache=$GOMODCACHE"`})
	res := c.sandboxed(context.Background(), `echo "cache=$GOMODCACHE"`, 0)
	want := filepath.Join(ws, ".anvil-cache", "proj-a", "go-mod")
	if !strings.Contains(res.Output, "cache="+want) {
		t.Errorf("the command's module cache: %q, want %s", res.Output, want)
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if _, err := cacheDir(Project{ID: bad}); err == nil {
			t.Errorf("a cache folder for project id %q", bad)
		}
	}
}

// The suggestion beside the check box comes from what the project is built
// with.
func TestTheCheckIsSuggestedFromTheProjectsFiles(t *testing.T) {
	for files, want := range map[string]string{
		"go.mod":         "go build ./... && go test ./...",
		"Cargo.toml":     "cargo build && cargo test",
		"pyproject.toml": "python3 -m pytest -q",
		`package.json={"scripts":{"test":"jest"}}`:                                        "npm install && npm test",
		`package.json={"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`: "",
		"README.md": "",
	} {
		dir := t.TempDir()
		name, body, _ := strings.Cut(files, "=")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := suggestCheck(dir); got != want {
			t.Errorf("%s: suggested %q, want %q", files, got, want)
		}
	}
}

// Whoever may save a project may set its check; one too long is refused.
func TestTheCheckIsSavedWithTheProject(t *testing.T) {
	do, get := anvilRoutes(t)
	long := strings.Repeat("x", maxCheckCommand+1)
	if code := do("root", "POST", "/api/projects", `{"id":"root1","name":"Tree","kind":"root","root":"`+t.TempDir()+`","check_command":"`+long+`"}`); code != 400 {
		t.Errorf("a check over the limit answered %d", code)
	}
	if code := do("root", "POST", "/api/projects", `{"id":"root1","name":"Tree","kind":"root","root":"`+t.TempDir()+`","check_command":"  go test ./...  ","check_manual":true}`); code >= 300 {
		t.Fatalf("saving the check answered %d", code)
	}
	if body := get("root", "/api/project/root1"); !strings.Contains(body, `"check_command":"go test ./..."`) || !strings.Contains(body, `"check_manual":true`) {
		t.Errorf("the saved project: %s", body)
	}
}

// The check card's script parses.
func TestTheCheckCardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	f := filepath.Join(t.TempDir(), "card.js")
	if err := os.WriteFile(f, []byte("var render = "+anvilCheckBlockJS+";\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
