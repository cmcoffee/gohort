// Package localexec provides a sandboxed local-shell tool. The LLM can
// run shell commands, but only in a per-user workspace directory and only
// after the user explicitly approves each call. ffmpeg-like read-only
// utilities, scripts the LLM wrote into the workspace, ad-hoc text
// processing — all reasonable. Anything that wants to reach outside the
// workspace is blocked at the path-resolution layer or visible to the
// user at the confirmation prompt before execution.
package localexec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// commandTimeout caps wall-clock time per shell call. Long-running
	// commands (`tail -f`, `top`, anything reading stdin) get killed.
	commandTimeout = 90 * time.Second

	// maxOutput is the per-command output cap. Beyond this, output is
	// truncated and a notice is appended so the LLM knows to narrow the
	// query rather than re-running expecting more.
	maxOutput = 10000
)

// RunLocalTool is no longer registered — the consolidated `local`
// grouped tool (in tools/files/local_grouped.go) provides the run
// action via the same RunSandboxedShell path. Implementation kept
// in case anything else calls it directly.
func init() {}

// RunLocalTool executes a shell command inside the caller's workspace
// sandbox. Mandatory user confirmation per call.
type RunLocalTool struct{}

func (t *RunLocalTool) Name() string { return "run_local" }

func (t *RunLocalTool) Desc() string {
	return "Run a shell command in your workspace sandbox. When bubblewrap is available the command runs in an isolated mount namespace where only the workspace is writable and only a minimal read-only set of system dirs is visible — commands cannot reach any user files, configs, or secrets outside the workspace. Network is allowed. Each call requires explicit user approval. Output is capped at 10,000 characters; the command is killed after 90 seconds."
}

func (t *RunLocalTool) Caps() []Capability {
	// A shell can do anything, so declare every cap. AllowedCaps gating
	// will hide this tool from sessions that don't permit CapExecute.
	return []Capability{CapExecute, CapRead, CapWrite, CapNetwork}
}

// NeedsConfirm forces every run_local invocation through the agent loop's
// user-confirmation prompt. The user sees the exact command before it runs.
func (t *RunLocalTool) NeedsConfirm() bool { return true }

func (t *RunLocalTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"command": {
			Type:        "string",
			Description: "The shell command to run inside the workspace sandbox. Standard sh -c semantics — pipes, redirects, and quoting work normally. Output (stdout+stderr combined) is returned.",
		},
	}
}

// Run is the no-session fallback. Without a session there's no workspace
// dir, so the tool refuses rather than running in some arbitrary cwd.
func (t *RunLocalTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("run_local requires a session with WorkspaceDir set; this caller did not provide one")
}

// RunWithSession is the real implementation. Reads sess.WorkspaceDir as
// the sandbox root; refuses if unset.
func (t *RunLocalTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("run_local requires a session with WorkspaceDir set")
	}
	cmd, _ := args["command"].(string)
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", fmt.Errorf("command is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	res := RunSandboxedShell(ctx, cmd, sess.WorkspaceDir)
	runErr := res.Err
	output := strings.TrimSpace(res.Output)

	if len(output) > maxOutput {
		totalLines := strings.Count(output, "\n") + 1
		truncated := output[:maxOutput]
		shownLines := strings.Count(truncated, "\n") + 1
		output = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars). "+
				"Pipe through grep/sed/head to narrow the result.]",
			shownLines, totalLines, len(output),
		)
	}

	if ctx.Err() == context.DeadlineExceeded {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed. Use a bounded variant if the command does not terminate on its own.]", commandTimeout)
		if output == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return output + notice, nil
	}

	if runErr != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if asExitErr, ok := runErr.(*exec.ExitError); ok {
			exitErr = asExitErr
			exitCode = exitErr.ExitCode()
		}
		if output == "" {
			return fmt.Sprintf("[exit code %d — no output]", exitCode), nil
		}
		return output + fmt.Sprintf("\n[exit code %d]", exitCode), nil
	}
	return output, nil
}

