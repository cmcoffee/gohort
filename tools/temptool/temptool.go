// Package temptool provides three meta-tools the LLM can use to define,
// list, and remove session-scoped tools at runtime:
//
//   - create_temp_tool: register a new tool whose body is a shell command
//     template. Visible to the LLM on the next round and from then on.
//   - list_temp_tools:   inspect what's currently defined.
//   - delete_temp_tool:  remove one by name.
//
// Temp tools execute through the same sandbox as run_local
// (RunSandboxedShell), so they inherit the bubblewrap mount-namespace
// isolation when bwrap is available. They cannot escape the workspace
// or read files outside it. They CAN make network calls (curl an API,
// download a font) — gate at the AllowedCaps tier if that's not desired.
//
// All three tools require CapExecute. The temp tool a session defines
// also runs at CapExecute. Runtime tool registration cannot grant the
// LLM capabilities it didn't already have.
package temptool

import (
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// commandTimeout caps wall-clock time per temp-tool invocation. Same as
// run_local — long-running commands get killed.
const commandTimeout = 90 * time.Second

// Pipeline-mode custom tools run a nested sub-agent, which — unlike the shell
// (commandTimeout) and api (SecureAPI 30s) paths — had NO wall-clock ceiling
// (it ran on context.Background(), bounded only by max_rounds). A stalled or
// looping nested agent then hangs the parent turn indefinitely, since the agent
// loop invokes tool handlers without a timeout. This tunable caps it.
func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_pipeline_tool_timeout",
		Category: "Timeouts",
		Label:    "Pipeline tool wall-clock timeout",
		Help:     "Max wall-clock time a pipeline-mode custom tool's nested sub-agent may run before it is cancelled. Bounds a runaway or stalled pipeline so it fails cleanly instead of hanging the turn.",
		Kind:     KindSeconds,
		Default:  300,
		Min:      30,
		Max:      1800,
	})
}

// maxOutput is the per-call output cap for shell-mode temp tools and
// for response_pipe filtered output on api-mode temp tools. Bumped
// from 10000 (run_local-compatible) to 50000 (~12K tokens) to give
// pipe projections enough headroom for richer results — a list of
// 50 records with several fields each would clip at 10K but fits
// comfortably at 50K, while still being well within a 200K context
// window. Shell-mode tools also benefit when their output is
// genuinely structured. run_local stays at 10000 — that's plain
// shell output where the readability ceiling is lower.
const maxOutput = 50000

// Individual tools (CreateTempToolTool, ListTempToolsTool,
// DeleteTempToolTool, CreateAPIToolTool) are no longer registered —
// the consolidated tool_def grouped tool (registered in tool_def.go)
// covers all four. Their implementations remain so tool_def.go's
// dispatchers can call them; just dropped from the catalog.
func init() {
	// Backfill a legacy tool's script into its exported bundle. New tools
	// capture the script into the record at authoring time (see the
	// create path), but tools authored before that — via local(write) + a
	// {workspace_dir} command_template reference — have an empty ScriptBody
	// and their script lives only in the owner's workspace on disk. Read it
	// back here so exports carry it. Best-effort: single on-disk script,
	// simple filename, owner's user-root workspace.
	ResolveToolScriptForExport = captureExportScript
}
