package temptool

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
)

// A shell tool, or a toolbox action that runs a command, called by an agent in
// a turn runs under core.BackgroundRun: inline until the detach time, then in
// the background, stopped only when it goes quiet past the no-output limit (or
// reaches its own declared timeout). See core/bgrun.
//
// Only the AGENT handlers take this path. A test button, an authoring probe or
// a script calling a tool directly (DispatchTempToolDirect) wants the result
// back in hand, keeps the fixed limit, and never detaches.

// runsCommand reports whether this call runs a local command: a shell tool, or
// the toolbox action args name when that action has a command template. A
// persistent shell is a session of short prompts, not one long run, and an
// api or pipeline call is bounded by its own timeouts.
func runsCommand(tt *TempTool, args map[string]any) bool {
	if tt == nil {
		return false
	}
	switch effectiveTempToolMode(*tt) {
	case TempToolModeShell:
		return true
	case TempToolModeToolbox:
		name := strings.TrimSpace(StringArg(args, "action"))
		for i := range tt.Actions {
			if tt.Actions[i].Name == name {
				return strings.TrimSpace(tt.Actions[i].CommandTemplate) != ""
			}
		}
	}
	return false
}

// dispatchAsAgent is dispatchTempTool for an agent's call: a command runs as a
// background-capable run, anything else as before. call does the dispatch on
// the session it is handed, which for a command is one that outlives the turn.
func dispatchAsAgent(sess *ToolSession, tt *TempTool, args map[string]any, label string, call func(*ToolSession) (string, error)) (string, error) {
	if sess == nil || !runsCommand(tt, args) {
		return call(sess)
	}
	return bgrun.BackgroundRun{Tool: label, Run: call}.Start(sess)
}

// isBackgroundNotice reports whether out is the framework's "moved to the
// background" notice rather than what the tool printed.
func isBackgroundNotice(out string) bool {
	_, marked := TakeFrameworkResultMark(out)
	return marked
}
