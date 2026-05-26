// Persistent-shell mode for temp tools.
//
// Builder-authored temp tools with Mode=="persistent" host a long-lived
// shell process (bash, psql, ssh, etc.) inside a long-lived bwrap.
// State (env vars, working dir, mounted FS context, login session)
// persists across LLM tool calls — unlike one-shot shell mode where
// each call mints a fresh bwrap. Lets the LLM hold an interactive
// session over many turns.
//
// LLM-facing surface: action-based dispatch on a single tool name.
//
//   tool_name(action="send", input="ls -la")      → output + complete flag
//   tool_name(action="read", timeout=5)           → buffered output
//   tool_name(action="interrupt")                 → SIGINT current command
//   tool_name(action="close")                     → tear down
//   tool_name(action="help")                      → usage details
//
// Open is lazy — the first "send" auto-opens the connection. Explicit
// "open" is for warm-up. Close happens explicitly OR on session end
// (caller-driven via TerminateSessionShells).
//
// Output handling: send blocks up to PersistentSendTimeoutSec (default
// 5s) waiting for PersistentPromptPattern to appear in stdout. If the
// pattern appears, complete=true and all output up to (and excluding)
// the prompt is returned. If timeout fires first, complete=false and
// whatever output has accumulated is returned; LLM should call
// action=read to drain more until the command finishes.

package temptool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// defaultPersistentSendTimeout is the bounded-wait the send action
	// uses when the tool author didn't set PersistentSendTimeoutSec.
	// Long enough for most "normal" commands to complete; short enough
	// that the LLM gets a responsive complete=false signal on slow
	// commands (build / install / training).
	defaultPersistentSendTimeout = 5 * time.Second

	// maxBufferedOutput caps how much un-drained output a persistent
	// shell holds before back-pressure kicks in (drop oldest). Prevents
	// runaway `tail -f` style commands from exhausting memory.
	maxBufferedOutput = 256 * 1024
)

// persistentShell wraps a long-lived bwrap+shell process pair.
type persistentShell struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser // combined stdout+stderr from the bwrap process

	// promptRegex is compiled from TempTool.PersistentPromptPattern at
	// open time. Nil when the author didn't supply a pattern — send
	// then relies on timeout alone to decide when to return.
	promptRegex *regexp.Regexp

	// buf holds drained output the LLM hasn't yet consumed. Reader
	// goroutine appends; send/read pops.
	buf []byte
	bufCond *sync.Cond // signaled when new bytes arrive in buf

	// cancel terminates the bwrap process when close fires.
	cancel context.CancelFunc

	// closed is true after Close runs. Send/read return errors after.
	closed bool

	// label is the human-readable identifier used in logs and error
	// messages. Format: "<tool_name>@<session>".
	label string
}

// shellRegistry is the package-global map of open persistent shells.
// Keyed by registryKey(sess, toolName).
var shellRegistry sync.Map

// registryKey builds the lookup key for the (session, tool) pair.
// Uses ChatSessionID when set, falling back to Username. Both empty
// means "no usable session scope" — open returns an error in that
// case rather than silently sharing one shell across all callers.
func registryKey(sess *ToolSession, toolName string) string {
	if sess == nil {
		return ""
	}
	scope := sess.ChatSessionID
	if scope == "" {
		scope = sess.Username
	}
	if scope == "" {
		return ""
	}
	return scope + ":" + toolName
}

// dispatchPersistentShellTempTool is the action-dispatched entry
// point. Action is read from args; specific actions take additional
// args (input, timeout).
func dispatchPersistentShellTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess == nil {
		return "", errors.New("persistent shell requires a session")
	}
	if registryKey(sess, tt.Name) == "" {
		return "", errors.New("persistent shell needs a session with ChatSessionID or Username set; got neither")
	}
	action := strings.TrimSpace(StringArg(args, "action"))
	switch action {
	case "", "help":
		return persistentShellHelp(tt), nil
	case "open":
		_, err := ensurePersistentShell(sess, tt)
		if err != nil {
			return "", fmt.Errorf("open: %w", err)
		}
		return fmt.Sprintf("Opened persistent shell %q. Use action=\"send\" to run a command.", tt.Name), nil
	case "send":
		input := StringArg(args, "input")
		if strings.TrimSpace(input) == "" {
			return "", errors.New("send: input is required")
		}
		ps, err := ensurePersistentShell(sess, tt)
		if err != nil {
			return "", fmt.Errorf("send (open): %w", err)
		}
		return ps.send(input, persistentSendTimeout(tt))
	case "read":
		timeout := persistentSendTimeout(tt)
		if v, ok := args["timeout"].(float64); ok && v > 0 {
			timeout = time.Duration(v) * time.Second
		}
		ps := lookupPersistentShell(sess, tt.Name)
		if ps == nil {
			return "", errors.New("read: shell is not open — call action=\"send\" or action=\"open\" first")
		}
		return ps.read(timeout)
	case "interrupt":
		ps := lookupPersistentShell(sess, tt.Name)
		if ps == nil {
			return "", errors.New("interrupt: shell is not open")
		}
		if err := ps.interrupt(); err != nil {
			return "", fmt.Errorf("interrupt: %w", err)
		}
		return "Sent SIGINT to the current command.", nil
	case "close":
		key := registryKey(sess, tt.Name)
		ps := lookupPersistentShell(sess, tt.Name)
		if ps == nil {
			return "Shell was not open; nothing to close.", nil
		}
		ps.close()
		shellRegistry.Delete(key)
		return fmt.Sprintf("Closed persistent shell %q.", tt.Name), nil
	}
	return "", fmt.Errorf("persistent shell: unknown action %q (expected open | send | read | interrupt | close | help)", action)
}

// persistentSendTimeout pulls the per-tool timeout (or default).
func persistentSendTimeout(tt *TempTool) time.Duration {
	if tt.PersistentSendTimeoutSec > 0 {
		return time.Duration(tt.PersistentSendTimeoutSec) * time.Second
	}
	return defaultPersistentSendTimeout
}

// lookupPersistentShell returns the existing shell for (session,
// tool) or nil.
func lookupPersistentShell(sess *ToolSession, toolName string) *persistentShell {
	key := registryKey(sess, toolName)
	if key == "" {
		return nil
	}
	v, ok := shellRegistry.Load(key)
	if !ok {
		return nil
	}
	ps, _ := v.(*persistentShell)
	if ps == nil || ps.isClosed() {
		shellRegistry.Delete(key)
		return nil
	}
	return ps
}

// ensurePersistentShell returns the existing shell or opens a new one.
func ensurePersistentShell(sess *ToolSession, tt *TempTool) (*persistentShell, error) {
	if ps := lookupPersistentShell(sess, tt.Name); ps != nil {
		return ps, nil
	}
	open := strings.TrimSpace(tt.PersistentOpenCmd)
	if open == "" {
		return nil, errors.New("PersistentOpenCmd is required for persistent-mode tools (the shell command to launch the long-lived process)")
	}
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return nil, fmt.Errorf("workspace unavailable: %w", err)
	}
	var promptRE *regexp.Regexp
	if pat := strings.TrimSpace(tt.PersistentPromptPattern); pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("PersistentPromptPattern is not a valid regex: %w", err)
		}
		promptRE = re
	}

	// Launch the bwrap process holding the long-lived shell. Use a
	// background context that the cancelFunc tears down on close.
	ctx, cancel := context.WithCancel(context.Background())
	c, err := startBwrapShell(ctx, wsDir, open)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start bwrap shell: %w", err)
	}
	ps := &persistentShell{
		cmd:         c.cmd,
		stdin:       c.stdin,
		stdout:      c.stdout,
		promptRegex: promptRE,
		cancel:      cancel,
		label:       tt.Name + "@" + sess.ChatSessionID,
	}
	ps.bufCond = sync.NewCond(&ps.mu)

	// Reader goroutine drains stdout into ps.buf. Bounded by
	// maxBufferedOutput; oldest bytes dropped on overflow so the shell
	// never blocks waiting for the LLM to consume.
	go ps.reader()

	shellRegistry.Store(registryKey(sess, tt.Name), ps)
	Log("[temptool/persistent] opened shell %q (cmd=%q)", ps.label, open)
	return ps, nil
}

// bwrapShell bundles the started bwrap process + its pipes.
type bwrapShell struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// startBwrapShell launches bwrap with `sh -c <openCmd>` as the long-
// lived inner command. Same sandbox posture as one-shot shell mode
// (workspace bind, system dirs read-only, namespaces unshared) but
// the bwrap process is kept alive — the caller closes it via cancel.
//
// Note: this reaches into core's bwrap argv construction. Persistent
// shells live in the same sandbox category as one-shot shell mode;
// the only difference is lifetime.
func startBwrapShell(ctx context.Context, workspaceDir, openCmd string) (*bwrapShell, error) {
	// detectBwrap is unexported in core; for persistent shells we
	// shell out via `sh -c` directly when bwrap isn't available
	// (degraded posture, matches one-shot mode's bwrap-missing path).
	// We can't call into core's exact bwrap-detection path from here,
	// so probe via exec.LookPath.
	bwrap, err := exec.LookPath("bwrap")
	var c *exec.Cmd
	if err == nil && bwrap != "" {
		// Build the same bwrap argv shape RunSandboxedShell uses,
		// trailing with `sh -c <openCmd>`. We assemble inline rather
		// than reusing core helpers because they're unexported AND
		// because persistent shells may want different defaults
		// later (e.g., a per-tool bind-mount declaration for SSH
		// keys — phase 2 work).
		args := persistentBwrapArgv(workspaceDir, openCmd)
		c = exec.CommandContext(ctx, bwrap, args...)
	} else {
		// No bwrap. Match one-shot mode's degraded fallback: run via
		// host sh -c. This is a security regression vs. the sandboxed
		// path; deployments should install bubblewrap. Log once at
		// open so the operator sees it.
		Log("[temptool/persistent] WARNING: bwrap not found, running persistent shell unsandboxed (install bubblewrap to enable sandbox)")
		c = exec.CommandContext(ctx, "sh", "-c", openCmd)
		c.Dir = workspaceDir
	}
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	// Combine stdout + stderr into one pipe so the reader sees both
	// in a single ordered stream — same as how one-shot shell mode
	// merges them into one buffer.
	stdoutR, stdoutW := io.Pipe()
	c.Stdout = stdoutW
	c.Stderr = stdoutW
	if err := c.Start(); err != nil {
		stdin.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("start: %w", err)
	}
	// Close the write end once the process exits so the reader
	// goroutine sees EOF and can exit cleanly.
	go func() {
		_ = c.Wait()
		stdoutW.Close()
	}()
	return &bwrapShell{cmd: c, stdin: stdin, stdout: stdoutR}, nil
}

// persistentBwrapArgv mirrors core/sandbox_exec.go's bwrapArgv shape.
// Duplicated here because the core function is unexported AND because
// persistent shells will diverge in phase 2 (per-tool credential
// bind-mounts). For now: same isolation posture as one-shot shell.
func persistentBwrapArgv(workspaceDir, shellCmd string) []string {
	return []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--unshare-cgroup-try",
		"--bind", workspaceDir, workspaceDir,
		"--chdir", workspaceDir,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib32", "/lib32",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		"--ro-bind-try", "/etc/pki", "/etc/pki",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--",
		"sh", "-c", shellCmd,
	}
}

// reader drains stdout into ps.buf. Runs until the underlying pipe
// hits EOF (process exited) or the shell is closed.
func (ps *persistentShell) reader() {
	br := bufio.NewReader(ps.stdout)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			ps.appendOutput(buf[:n])
		}
		if err != nil {
			// EOF or pipe closed — mark shell closed so subsequent
			// sends fail cleanly instead of hanging.
			ps.mu.Lock()
			ps.closed = true
			ps.bufCond.Broadcast()
			ps.mu.Unlock()
			return
		}
	}
}

// appendOutput pushes new bytes into ps.buf, applying back-pressure.
func (ps *persistentShell) appendOutput(b []byte) {
	ps.mu.Lock()
	ps.buf = append(ps.buf, b...)
	if len(ps.buf) > maxBufferedOutput {
		// Drop oldest bytes — keep the most-recent maxBufferedOutput
		// bytes. Prevents `tail -f` style commands from exhausting
		// memory while the LLM hasn't consumed.
		drop := len(ps.buf) - maxBufferedOutput
		ps.buf = ps.buf[drop:]
	}
	ps.bufCond.Broadcast()
	ps.mu.Unlock()
}

// send writes the command (with trailing newline) and blocks until
// the prompt regex matches the tail of accumulated output OR the
// timeout fires. Returns the output captured up to (and excluding)
// the matched prompt, plus a complete flag.
func (ps *persistentShell) send(input string, timeout time.Duration) (string, error) {
	if ps.isClosed() {
		return "", errors.New("shell has closed (process exited)")
	}
	// Ensure trailing newline so the shell sees a full line.
	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}
	// Mark the start of "new" output so we don't include anything
	// that came before this send.
	ps.mu.Lock()
	startLen := len(ps.buf)
	ps.mu.Unlock()
	if _, err := ps.stdin.Write([]byte(input)); err != nil {
		return "", fmt.Errorf("write to shell: %w", err)
	}
	return ps.waitForPromptFrom(startLen, timeout)
}

// read pulls whatever's currently buffered (and waits briefly for
// any in-flight bytes). Used for slow commands where the previous
// send returned complete=false.
func (ps *persistentShell) read(timeout time.Duration) (string, error) {
	if ps.isClosed() {
		// Closed but might still have buffered output the LLM hasn't
		// drained yet — return that, then signal closed.
		out := ps.drainAll()
		if out != "" {
			return out + "\n[shell closed]", nil
		}
		return "", errors.New("shell has closed (process exited)")
	}
	ps.mu.Lock()
	startLen := 0 // read drains EVERYTHING, not just bytes since last send
	if len(ps.buf) > 0 {
		startLen = 0
	}
	ps.mu.Unlock()
	_ = startLen
	return ps.waitForPromptFrom(0, timeout)
}

// waitForPromptFrom waits until either the prompt regex matches in
// ps.buf[fromIdx:] OR the timeout fires. Returns the captured output
// and the complete flag.
func (ps *persistentShell) waitForPromptFrom(fromIdx int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		ps.mu.Lock()
		out := ps.buf[fromIdx:]
		if ps.promptRegex != nil {
			if loc := ps.promptRegex.FindIndex(out); loc != nil {
				captured := string(out[:loc[0]])
				// Consume up to and INCLUDING the prompt so the next
				// send starts after it.
				consumeEnd := fromIdx + loc[1]
				if consumeEnd > len(ps.buf) {
					consumeEnd = len(ps.buf)
				}
				ps.buf = ps.buf[consumeEnd:]
				ps.mu.Unlock()
				return formatShellOutput(captured, true), nil
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// Timeout — return whatever accumulated, leave it in
			// buf so a subsequent read can drain more.
			captured := string(out)
			ps.buf = ps.buf[fromIdx+len(out):]
			ps.mu.Unlock()
			return formatShellOutput(captured, false), nil
		}
		// Wait for new bytes OR timeout. Use a goroutine-driven
		// timer because sync.Cond doesn't support timed waits
		// natively.
		waitDone := make(chan struct{})
		go func() {
			ps.mu.Lock()
			ps.bufCond.Wait()
			close(waitDone)
			ps.mu.Unlock()
		}()
		ps.mu.Unlock()
		select {
		case <-waitDone:
			// new bytes — loop and re-check
		case <-time.After(remaining):
			// timeout — bufCond never fires, leak the goroutine.
			// Broadcast on next append OR close will release it.
		}
	}
}

// drainAll snapshots and clears the current buffer. Used by read
// when the shell has closed and we want to surface any final output.
func (ps *persistentShell) drainAll() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.buf) == 0 {
		return ""
	}
	out := string(ps.buf)
	ps.buf = ps.buf[:0]
	return out
}

// interrupt sends SIGINT to the underlying process. The shell sees
// it as Ctrl-C and aborts the current command. The shell itself
// keeps running.
func (ps *persistentShell) interrupt() error {
	if ps.isClosed() {
		return errors.New("shell has closed")
	}
	if ps.cmd == nil || ps.cmd.Process == nil {
		return errors.New("no process to interrupt")
	}
	return ps.cmd.Process.Signal(syscall.SIGINT)
}

// close tears down the persistent shell — closes stdin (signals EOF
// to the inner shell), cancels the context (which kills bwrap), and
// marks the wrapper closed.
func (ps *persistentShell) close() {
	ps.mu.Lock()
	if ps.closed {
		ps.mu.Unlock()
		return
	}
	ps.closed = true
	ps.bufCond.Broadcast()
	ps.mu.Unlock()
	if ps.stdin != nil {
		ps.stdin.Close()
	}
	if ps.cancel != nil {
		ps.cancel()
	}
	Log("[temptool/persistent] closed shell %q", ps.label)
}

// isClosed reports whether the shell has been torn down OR the
// underlying process exited (reader goroutine sets closed on EOF).
func (ps *persistentShell) isClosed() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.closed
}

// formatShellOutput returns the structured tool-result text the LLM
// sees on send/read. JSON would be cleaner but the tool result is
// already part of the LLM's text context — a labeled multi-line
// shape reads naturally without an extra parse step on the LLM side.
func formatShellOutput(captured string, complete bool) string {
	status := "running"
	if complete {
		status = "complete"
	}
	captured = strings.TrimRight(captured, "\n\r ")
	if captured == "" {
		captured = "(no output)"
	}
	return fmt.Sprintf("[status: %s]\n%s", status, captured)
}

// persistentShellHelp returns the action-usage description shown to
// the LLM when it calls action="help" (or omits action).
func persistentShellHelp(tt *TempTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — persistent shell. State (env vars, cwd, login session) persists across calls.\n\n", tt.Name)
	b.WriteString("ACTIONS:\n")
	b.WriteString("  send       Run a command. Args: input (the command). Returns output + [status: complete|running].\n")
	b.WriteString("  read       Drain more output when a prior send returned status=running. Args: timeout (optional, seconds).\n")
	b.WriteString("  interrupt  Send Ctrl-C to the current command (recovers from `vim`, kills a hung process).\n")
	b.WriteString("  open       Pre-open the shell. Optional — first send auto-opens.\n")
	b.WriteString("  close      Tear down. Also fires automatically when the session ends.\n")
	b.WriteString("\n")
	b.WriteString("STATUS FLAG: [status: complete] means the shell is ready for the next command.\n")
	b.WriteString("            [status: running] means the previous command is still producing output — call action=read to drain more.\n")
	return b.String()
}

// TerminateSessionShells closes every persistent shell registered for
// the given session. Called by host apps on session teardown so
// long-lived bwrap processes don't leak when a chat ends.
func TerminateSessionShells(sess *ToolSession) {
	if sess == nil {
		return
	}
	scope := sess.ChatSessionID
	if scope == "" {
		scope = sess.Username
	}
	TerminateSessionShellsByScope(scope)
}

// TerminateSessionShellsByScope is the host-app variant for places
// where the live ToolSession isn't available (e.g. session-deletion
// handlers operating on a session ID without an active runtime). The
// scope string should be whatever the host uses as registryKey's
// prefix (chat session ID, username, etc.).
func TerminateSessionShellsByScope(scope string) {
	if scope == "" {
		return
	}
	prefix := scope + ":"
	shellRegistry.Range(func(k, v any) bool {
		key, _ := k.(string)
		if !strings.HasPrefix(key, prefix) {
			return true
		}
		if ps, ok := v.(*persistentShell); ok && ps != nil {
			ps.close()
		}
		shellRegistry.Delete(key)
		return true
	})
}
