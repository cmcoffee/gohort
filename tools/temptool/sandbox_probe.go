// sandbox_probe — narrow, no-confirmation tool that lets Builder
// check whether a binary is available in the shell-mode sandbox
// before authoring a tool that depends on it.
//
// The check runs `command -v <name>` in the same bubblewrap sandbox
// that temp-tool dispatch uses, so what this tool sees matches what
// an authored tool would see at dispatch time. Reuse of the same
// /usr ro-bind means the answer is reliable across the two paths.
//
// Why not just have the LLM use local() for this?
//   local() requires user confirmation per call. Probing 3-5 binaries
//   during a design phase would interrupt the user 3-5 times for
//   trivial yes/no queries. This tool is purpose-built: takes only a
//   binary name (validated as identifier-only — no shell metachars
//   possible), runs a single fixed command, returns the answer. Zero
//   injection surface; zero reason to require confirmation.

package temptool

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// (Registration dropped — sandbox_probe's logic folded into
// workspace(action="probe"). The struct + methods stay so any
// stale agent record naming "sandbox_probe" in AllowedTools just
// resolves to nothing — functionally lost, but workspace(probe)
// provides the same capability without a separate catalog slot.)
// func init() { RegisterChatTool(new(SandboxProbeTool)) }

// SandboxProbeTool answers "is binary X installed in the sandbox?"
// without exposing arbitrary shell execution.
type SandboxProbeTool struct{}

func (t *SandboxProbeTool) Name() string { return "sandbox_probe" }

func (t *SandboxProbeTool) Desc() string {
	return "Check whether a binary is available in the shell-mode tool sandbox. Returns the path if found, or a 'not available' message if not. Use this BEFORE authoring a shell-mode tool that depends on a non-POSIX binary (ImageMagick's `convert`, `ffmpeg`, `yt-dlp`, etc.) — if the probe says not available, the tool will fail at dispatch, so pivot to a different design. Safe and cheap; no user confirmation required."
}

// Caps: probing only reads system state (which binary is at /usr/bin/X).
// No write, no network, no exec beyond `command -v` itself.
func (t *SandboxProbeTool) Caps() []Capability {
	return []Capability{CapRead}
}

// NeedsConfirm = false. The tool is scope-limited to a fixed
// `command -v <validated-name>` shell — there's no LLM-supplied
// command surface, no injection vector, no destructive action. Asking
// the user to approve "check if ffmpeg exists" would be silly noise.
func (t *SandboxProbeTool) NeedsConfirm() bool { return false }

func (t *SandboxProbeTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {
			Type:        "string",
			Description: "Binary name to probe (e.g. \"ffmpeg\", \"convert\", \"yt-dlp\", \"python3\"). Identifier-only: letters, digits, underscores, dashes. No paths, no shell metachars.",
		},
	}
}

// validProbeName: letters/digits/underscore/dash, length 1-64.
// Mirrors typical Unix binary naming. Rejects anything that could
// inject shell metachars or escape the `command -v` invocation.
var validProbeName = regexp.MustCompile(`^[a-zA-Z0-9_\-+.]{1,64}$`)

func (t *SandboxProbeTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("sandbox_probe requires a session context")
}

func (t *SandboxProbeTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validProbeName.MatchString(name) {
		return "", fmt.Errorf("invalid binary name %q — must be identifier characters only (letters, digits, _, -, +, .)", name)
	}

	// Run in a tight ephemeral sandbox. Path-only workspace doesn't
	// matter (probe doesn't write anything); we use a tmpfs cwd.
	// Use RunSandboxedShellPipe — it gives us the same /usr bind as
	// the temp-tool dispatch but with no writable mount + no network
	// (we don't need either for `command -v`).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := "command -v " + name + " 2>/dev/null || true"
	res := RunSandboxedShellPipe(ctx, cmd, "")
	output := strings.TrimSpace(res.Output)
	if output == "" {
		return fmt.Sprintf("%q is NOT available in the sandbox. Pivot your design — either use a different binary or switch the tool's mode (api / pipeline / different shell tool).", name), nil
	}
	return fmt.Sprintf("%q is available at %s. Safe to use in shell-mode tools.", name, output), nil
}
