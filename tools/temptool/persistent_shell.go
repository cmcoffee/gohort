// Persistent-shell mode for temp tools.
//
// Builder-authored temp tools with Mode=="persistent" host a long-lived
// shell process (bash, psql, ssh, etc.) inside a long-lived sandbox.
// State (env vars, working dir, mounted FS context, login session)
// persists across LLM tool calls — unlike one-shot shell mode where
// each call mints a fresh one. Lets the LLM hold an interactive
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

// persistentShell wraps a long-lived sandbox+shell process pair.
type persistentShell struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser // combined stdout+stderr from the sandboxed process

	// promptRegex is compiled from TempTool.PersistentPromptPattern at
	// open time. Nil when the author didn't supply a pattern — send
	// then relies on timeout alone to decide when to return.
	promptRegex *regexp.Regexp

	// buf holds drained output the LLM hasn't yet consumed. Reader
	// goroutine appends; send/read pops.
	buf     []byte
	bufCond *sync.Cond // signaled when new bytes arrive in buf

	// cancel terminates the sandboxed process when close fires.
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

	// Launch the sandboxed process holding the long-lived shell. Use a
	// background context that the cancelFunc tears down on close.
	//
	// Background rather than the caller's ctx because the shell must outlive
	// the tool call that opened it — that is the whole feature.
	//
	// But the sandbox reads two things off the context that a bare Background
	// does not carry, and neither used to be stamped here:
	//
	//   ContextWithNetworkConnector — the session's Private-mode cap. This
	//     function has always CLAIMED to honor it ("any new persistent shell
	//     spawned while Private is on must respect that"), and never did:
	//     NetworkAllowedFromContext was asked of a context derived from
	//     Background, where a missing connector reads as allowed. Private mode
	//     cut network for every one-shot command and silently left it up for
	//     the long-lived shell.
	//
	//   ContextWithSandboxCaller — the admin stamp the "admin" bypass keys on.
	//     Unstamped is non-admin, so this direction was merely strict rather
	//     than wrong, but on a host running BypassAdmin it refused the
	//     operator's own persistent shell for no stated reason.
	//
	// Stamped from the session rather than threaded through the dispatch
	// signature: both facts are properties of WHO opened the shell, and the
	// session is the thing that knows.
	ctx, cancel := context.WithCancel(context.Background())
	ctx = sess.ContextWithNetworkConnector(ctx)
	ctx = sess.ContextWithSandboxCaller(ctx)
	c, err := startSandboxedShell(ctx, wsDir, open)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open persistent shell: %w", err)
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
	Log("[temptool/persistent] opened shell %q (cmd=%q backend=%s confined=%v)", ps.label, open, c.backend, c.confined)
	return ps, nil
}

// sandboxedShell bundles the started process + its pipes, and the backend that
// confined it (reported at open so a later question about what was running has
// an answer in the log).
type sandboxedShell struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// backend is the confinement mechanism that built cmd ("bubblewrap",
	// "seatbelt", "none"), and confined is whether it isolates anything.
	backend  string
	confined bool
}

// startSandboxedShell launches the long-lived inner shell under whatever
// backend this host has, and hands back its pipes. Same confinement posture as
// one-shot shell mode; the only difference is that the process outlives the
// call.
//
// It builds NOTHING itself. This used to probe exec.LookPath("bwrap") and
// assemble a private copy of the bwrap argv (persistentBwrapArgv), on the
// reasoning that core's helpers were unexported and persistent shells might
// diverge later. Both halves aged badly. The copy never learned about the
// second backend, so on macOS it found no bwrap and took its fallback; and the
// fallback was the real problem — it logged a warning and ran the shell through
// the host's `sh -c`, unconfined, at the service account's full privilege.
// Every one-shot command on that same host was being REFUSED for exactly that
// condition, because core/sandbox's bypass policy is fail-closed by default.
// A long-lived LLM-driven shell was the one thing that walked past it.
//
// So the refusal now comes from the same place as everyone else's, and a new
// backend reaches persistent shells with no work here at all.
func startSandboxedShell(ctx context.Context, workspaceDir, openCmd string) (*sandboxedShell, error) {
	// Network policy: persistent-mode tools NEED network by design
	// (psql, redis-cli, ssh REPLs are all connection-oriented). Default
	// is allowed. But the session connector still acts as a hard cap:
	// Private mode toggles network OFF at the session level, and any
	// new persistent shell spawned while Private is on must respect
	// that. The shell is checked at OPEN time only; a session that flips
	// to Private after open-time can't re-namespace the running process
	// — to enforce mid-flight, the chat UI cancels in-flight dispatches
	// on toggle. The connector rides ctx, which is what the builder reads.
	built, err := NewSandboxedShellCmd(ctx, openCmd, workspaceDir, nil)
	if err != nil {
		// The fail-closed refusal. Returned rather than logged-and-degraded:
		// the operator needs the sentence, and the LLM needs to not get a
		// shell it was never supposed to have.
		return nil, err
	}
	if !built.Confined {
		// Reachable only when the deployment explicitly opted out
		// (GOHORT_ALLOW_UNSANDBOXED). core/sandbox has already logged the
		// warning once; this names the shell that took the exemption, because
		// a long-lived one is the one worth being able to find later.
		Log("[temptool/persistent] WARNING: opening persistent shell UNCONFINED (backend=%s) — the deployment permits it", built.Backend)
	}
	c := built.Cmd
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
	return &sandboxedShell{cmd: c, stdin: stdin, stdout: stdoutR, backend: built.Backend, confined: built.Confined}, nil
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
// to the inner shell), cancels the context (which kills the sandbox), and
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
// long-lived sandboxed processes don't leak when a chat ends.
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
