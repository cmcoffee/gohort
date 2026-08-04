package servitor

import (
	"context"
	"fmt"
	"strings"
)

// A run's scratch directory is the one place on a target where the agent may
// write freely. Investigations legitimately need somewhere to stage a script,
// spool a long command's output, or park an intermediate file — and the risk
// gate has to let the CLEANUP through too, or a "read-only" run ends up dirtier
// than a normal one: it creates the files and is then denied the `rm`.
//
// So every run gets a private directory, the classifier ungates writes and
// deletes inside it (see classify_command_scoped), and teardown is the
// coordinator's job rather than something the model has to remember. Removal
// runs through the RAW exec path, never the gated tool, so it cannot be
// refused by the very gate that made the directory necessary.
//
// The directory is per-run, not per-appliance: two concurrent sessions against
// the same host — including a workspace fanning one question out across a
// cluster — never share a workspace or race each other's cleanup.

// scratch_prefix is the fixed leading path component, so an operator can spot
// (and sweep) anything this app left behind with a single glob.
const scratch_prefix = "/tmp/servitor-"

// scratch_dir returns the private working directory for a run. The session ID
// is reduced to path-safe characters so a caller-supplied ID can never break
// out of the prefix or inject shell syntax into the setup/teardown commands.
func scratch_dir(sessionID string) string {
	var b strings.Builder
	for i := 0; i < len(sessionID); i++ {
		c := sessionID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "run"
	}
	if len(id) > 64 {
		id = id[:64]
	}
	return scratch_prefix + id
}

// scratch_exec is the raw command runner for a target — a.exec_command_ctx for
// SSH appliances, a.exec_local_ctx for local command appliances. Setup and
// teardown deliberately bypass the risk gate.
type scratch_exec func(ctx context.Context, cmd string) (string, error)

// scratch_setup creates the run's scratch directory on the target. A failure is
// not fatal: the run continues without a sanctioned write location, which just
// means every write gates as it did before.
func scratch_setup(ctx context.Context, run scratch_exec, dir string) error {
	if run == nil || dir == "" {
		return fmt.Errorf("scratch: no target")
	}
	out, err := run(ctx, fmt.Sprintf("mkdir -p %s && chmod 700 %s", dir, dir))
	if err != nil {
		return fmt.Errorf("scratch: %w", err)
	}
	// mkdir is silent on success; any output means it failed to create the dir
	// (read-only /tmp, quota, permissions) even though the shell exited 0.
	if strings.Contains(out, "exit code") || strings.Contains(strings.ToLower(out), "denied") {
		return fmt.Errorf("scratch: %s", strings.TrimSpace(out))
	}
	return nil
}

// scratch_teardown removes the run's scratch directory. Called on every exit
// path, including cancellation, so a fan-out that touched three nodes leaves
// nothing behind on any of them. Uses a detached context: the session context
// is usually already cancelled by the time cleanup runs.
func scratch_teardown(run scratch_exec, dir string) {
	if run == nil || dir == "" || !strings.HasPrefix(dir, scratch_prefix) {
		return // never rm -rf a path this package did not construct
	}
	ctx, cancel := context.WithTimeout(context.Background(), command_timeout())
	defer cancel()
	run(ctx, "rm -rf "+dir)
}

// scratch_guidance is the worker-prompt block that points temp files at the
// scratch directory. Without it the model writes to /tmp or the app directory
// and trips the gate on work that should be friction-free.
func scratch_guidance(dir string) string {
	if dir == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Scratch directory\n\n")
	fmt.Fprintf(&b, "`%s` is yours for this session. Write every temp file, script, and spooled command output there — writing and deleting inside it never needs approval, and it is removed automatically when the session ends, so you do not need to clean it up yourself.\n\n", dir)
	b.WriteString("Writing ANYWHERE else — including a redirect or `tee` onto an existing file — modifies the system and will stop for operator approval. Never redirect over a real file to \"test\" something.\n\n")
	return b.String()
}
