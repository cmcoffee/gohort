package anvil

// The project's check: its build and its tests, run in the sandbox.
//
// A coding agent is only as good as the loop it gets: change, build, read the
// failure, fix. Without a check it changes code it never builds, and reports
// success it never observed. So a project names its check (CheckCommand), the
// agent runs it with the check tool as it goes, and after the agent's edits
// Anvil runs it before the turn may end (finishCheck): a failing check sends
// the reply back with what failed, within the loop's correction budget. A
// project whose tests are slow can ask for the check only when the agent
// runs it (CheckManual).
//
// Unlike project commands (actions.go), the check runs in the sandbox, so
// anyone who may edit the project may set it. What it gets that the plain
// sandbox does not: a package cache per project, kept outside the checkout,
// and downloads through gohort's relay (public addresses, package
// registries and sites the owner trusts), so `go build` or `npm install`
// on a fresh checkout can fetch what it needs. See
// docs/design/anvil-development.md.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/sandbox"
)

const (
	// checkTimeout bounds one run of the check.
	checkTimeout = 10 * time.Minute
	// maxCheckOutput is how much of a check's output the agent is shown: the
	// end, where a build or a test run says what failed.
	maxCheckOutput = 6000
	// maxCheckCommand bounds the command a project may store.
	maxCheckCommand = 2000
	// checkBlock is the card a check run publishes.
	checkBlock = "anvil_check"
)

// cacheDir is where p's packages are kept between runs: per project, so one
// project cannot plant something another builds with, and outside every
// checkout and every user's workspace, so nothing but this project's own
// runs can write it.
func cacheDir(p Project) (string, error) {
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured")
	}
	if p.ID == "" || strings.ContainsAny(p.ID, `/\`) || strings.Contains(p.ID, "..") {
		return "", fmt.Errorf("invalid project id %q", p.ID)
	}
	return filepath.Join(base, ".anvil-cache", p.ID), nil
}

// removeCache deletes p's package cache. Go keeps its module cache read-only,
// so its folders are made writable first.
func removeCache(p Project) {
	dir, err := cacheDir(p)
	if err != nil {
		return
	}
	removeAllWritable(dir)
}

// removeAllWritable deletes dir after making every folder in it writable.
func removeAllWritable(dir string) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// toolchainEnv points each toolchain's caches into dir. GOTOOLCHAIN=local
// keeps go from fetching a newer toolchain a go.mod asks for.
func toolchainEnv(dir string) map[string]string {
	return map[string]string{
		"GOCACHE":          filepath.Join(dir, "go-build"),
		"GOMODCACHE":       filepath.Join(dir, "go-mod"),
		"GOPATH":           filepath.Join(dir, "go"),
		"GOTOOLCHAIN":      "local",
		"npm_config_cache": filepath.Join(dir, "npm"),
		"PIP_CACHE_DIR":    filepath.Join(dir, "pip"),
		"CARGO_HOME":       filepath.Join(dir, "cargo"),
		"XDG_CACHE_HOME":   filepath.Join(dir, "xdg"),
	}
}

// sandboxed runs command in the project root inside the sandbox, with the
// project's package cache and, when the turn has a tool session, downloads
// through gohort's relay. ctx is the turn's, so a private turn's privacy and
// the agent's network ceiling apply, and stopping the turn stops the command.
func (c *toolCtx) sandboxed(ctx context.Context, command string, timeout time.Duration) sandbox.SandboxedShellResult {
	if ctx == nil {
		ctx = context.Background()
	}
	spec := sandbox.ShellRun{Command: command, WorkspaceDir: c.base}
	if dir, err := cacheDir(c.project); err == nil && os.MkdirAll(dir, 0o700) == nil {
		spec.Env = toolchainEnv(dir)
		spec.Writable = []string{dir}
	}
	if sess := ToolSessionFromContext(ctx); sess != nil {
		spec.Proxy = true
		spec.HookCapabilities = []string{"proxy"}
		spec.HookSession = sess
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return sandbox.RunSandboxedShellIn(ctx, spec)
}

// checkOutcome is how one run of the check went.
type checkOutcome struct {
	ok       bool
	output   string // the end of it, at most maxCheckOutput
	took     time.Duration
	timedOut bool
}

// hasCheck reports whether the agent may run this project's check.
func (c *toolCtx) hasCheck() bool {
	return strings.TrimSpace(c.project.CheckCommand) != "" && !c.readOnly
}

// markEdited records that the tree changed since the check last passed.
func (c *toolCtx) markEdited() {
	c.checkMu.Lock()
	c.editedSinceCheck = true
	c.checkMu.Unlock()
}

// edited reports whether the tree changed since the check last passed.
func (c *toolCtx) edited() bool {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	return c.editedSinceCheck
}

// noteCtx keeps the turn's context from the latest tool call, for the check
// the turn runs as it ends (finishCheck has no context of its own).
func (c *toolCtx) noteCtx(ctx context.Context) {
	c.checkMu.Lock()
	c.turnCtx = ctx
	// A conversation's first message is sent with no session id: the turn
	// gets its id as it starts, and its tools learn it here, so the changes
	// they record name the session that made them (a revert halts it).
	if c.sessionID == "" {
		if ts := ToolSessionFromContext(ctx); ts != nil && ts.ChatSessionID != "" {
			c.sessionID = ts.ChatSessionID
		}
	}
	c.checkMu.Unlock()
}

// session is the turn's session id, as noteCtx last learned it.
func (c *toolCtx) session() string {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	return c.sessionID
}

func (c *toolCtx) lastCtx() context.Context {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	if c.turnCtx == nil {
		return context.Background()
	}
	return c.turnCtx
}

// runCheck runs the check, showing it on the page while it runs and how it
// ended after. A pass clears the edited mark.
func (c *toolCtx) runCheck(ctx context.Context) checkOutcome {
	command := strings.TrimSpace(c.project.CheckCommand)
	c.checkMu.Lock()
	c.checkSeq++
	id := fmt.Sprintf("anvil-check-%s-%d", c.sessionID, c.checkSeq)
	c.checkMu.Unlock()
	start := time.Now()
	c.publishCheck(id, command, "running", "", start, 0)
	res := c.sandboxed(ctx, command, checkTimeout)
	o := checkOutcome{
		ok:       res.Err == nil && !res.TimedOut,
		output:   tailOutput(res.Output, maxCheckOutput),
		took:     time.Since(start),
		timedOut: res.TimedOut,
	}
	if res.Err != nil && strings.TrimSpace(o.output) == "" {
		o.output = res.Err.Error()
	}
	state := "passed"
	if !o.ok {
		state = "failed"
	}
	c.publishCheck(id, command, state, o.output, start, o.took)
	if o.ok {
		c.checkMu.Lock()
		c.editedSinceCheck = false
		c.checkMu.Unlock()
	}
	return o
}

// publishCheck puts the check's card in the conversation, or updates it.
func (c *toolCtx) publishCheck(id, command, state, output string, start time.Time, took time.Duration) {
	if c.emitBlock == nil {
		return
	}
	c.emitBlock(agenthost.Block{
		Type:  checkBlock,
		ID:    id,
		Title: command,
		Text:  state,
		Data: map[string]string{
			"state":   state,
			"output":  output,
			"started": start.UTC().Format(time.RFC3339),
			"took":    fmt.Sprintf("%.0f", took.Seconds()),
		},
	})
}

// finishCheck is the turn's condition on ending (agenthost.Publisher's
// FinishCheck): after the agent's edits, the check has to pass, or the reply
// goes back with what failed. Nothing is run when nothing was edited, when
// there is no check, or when the project asks for the check only on request.
//
// An app project then installs or previews what the turn changed
// (app_project.go), once the check passes. Whether the turn changed anything
// is read first: a passing check clears it.
func (c *toolCtx) finishCheck(string) string {
	edited := c.edited()
	if c.hasCheck() && !c.project.CheckManual && edited {
		if o := c.runCheck(c.lastCtx()); !o.ok {
			return checkFailedNote(c.project.CheckCommand, o)
		}
	}
	return c.appFinish(edited)
}

// checkFailedNote is what the agent is told when its turn ends on a failing
// check.
func checkFailedNote(command string, o checkOutcome) string {
	what := "fails"
	if o.timedOut {
		what = fmt.Sprintf("did not finish within %s", checkTimeout)
	}
	return fmt.Sprintf("Before you finish: this project's check (%s) %s after your changes.\n\n%s\n\n"+
		"Fix what it reports and run the check tool again. If you cannot make it pass, say plainly in your reply what still fails and why.",
		strings.TrimSpace(command), what, o.output)
}

// checkTool runs the project's check on request.
func (c *toolCtx) checkTool() AgentToolDef {
	command := strings.TrimSpace(c.project.CheckCommand)
	return AgentToolDef{
		Tool: Tool{
			Name: "check",
			Description: "Run this project's check (" + command + "): its build and tests, in the sandbox. " +
				"Run it after a change and before you say the work is done; a change is not finished while the check fails.",
			Parameters: map[string]ToolParam{},
			Caps:       []Capability{CapExecute},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			o := c.runCheck(ctx)
			secs := o.took.Round(time.Second)
			if o.ok {
				return fmt.Sprintf("The check passed (%s) in %s.\n%s", command, secs, tailOutput(o.output, 1500)), nil
			}
			if o.timedOut {
				return fmt.Sprintf("The check did not finish within %s (%s):\n%s", checkTimeout, command, o.output), nil
			}
			return fmt.Sprintf("The check failed (%s) after %s:\n%s", command, secs, o.output), nil
		},
	}
}

// tailOutput is the end of s, at most n bytes, cut at a line.
func tailOutput(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return "...(earlier output trimmed)...\n" + cut
}

// suggestCheck names a check from what the project's files say it is built
// with, or "" when they do not say.
func suggestCheck(base string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(base, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		return "go build ./... && go test ./..."
	case has("Cargo.toml"):
		return "cargo build && cargo test"
	case has("package.json"):
		if b, err := os.ReadFile(filepath.Join(base, "package.json")); err == nil && strings.Contains(string(b), `"test"`) && !strings.Contains(string(b), "no test specified") {
			return "npm install && npm test"
		}
		return ""
	case has("pyproject.toml"), has("setup.py"), has("pytest.ini"), has("setup.cfg"):
		return "python3 -m pytest -q"
	case has("Makefile"):
		if b, err := os.ReadFile(filepath.Join(base, "Makefile")); err == nil && strings.Contains(string(b), "\ntest:") {
			return "make test"
		}
	}
	return ""
}
