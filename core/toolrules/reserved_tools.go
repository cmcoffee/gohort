// Reserved tool names — names claimed by DYNAMIC, per-agent framework tools
// (channel messaging tools, operator / fleet tools) that are assembled at
// dispatch time and so never appear in the static RegisteredChatTools catalog.
//
// A user- or agent-authored temp tool must NOT take one of these names. If it
// does, it SHADOWS the real, delivering tool with a stub: a call to e.g.
// send_message hits the fake (which typically reports "Sent" but never routes
// through Bridges) and silently fails to deliver — observed exactly this way.
// temptool creation checks IsReservedToolName in addition to the static catalog,
// and temp-tool hydration drops a colliding name so an already-authored shadow
// stops overriding the built-in.
package toolrules

import (
	"strings"
	"sync"
)

var (
	reservedToolNames   = map[string]bool{}
	reservedToolNamesMu sync.RWMutex
)

// RegisterReservedToolName marks names as reserved built-in tool names. Called at
// startup by the app that provides the dynamic tools.
func RegisterReservedToolName(names ...string) {
	reservedToolNamesMu.Lock()
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			reservedToolNames[n] = true
		}
	}
	reservedToolNamesMu.Unlock()
}

// IsReservedToolName reports whether a name belongs to a dynamic built-in tool
// (and so must not be claimed by a temp tool).
func IsReservedToolName(name string) bool {
	reservedToolNamesMu.RLock()
	defer reservedToolNamesMu.RUnlock()
	return reservedToolNames[strings.TrimSpace(name)]
}

// --- workflow control plane ---------------------------------------------------
//
// The tools a machine step uses to hand on: change_phase and its siblings.
// Registered by the app that supplies them, for the same reason the reserved
// names above are — they are assembled per turn and never appear in a static
// catalog, so nothing else can enumerate them.
//
// Asked by machine-definition validation, which must refuse a step that denies
// its own way out: a step that cannot change_phase is stranded, not restricted.
// Kept apart from the reserved set because that one is much broader — denying
// send_message for a step is a perfectly sensible thing to author, denying
// change_phase is never sensible.

var workflowControlTools = map[string]bool{}

// RegisterWorkflowControlTool marks names as the machine control plane. Called
// at startup by the app that provides them.
func RegisterWorkflowControlTool(names ...string) {
	reservedToolNamesMu.Lock()
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			workflowControlTools[n] = true
		}
	}
	reservedToolNamesMu.Unlock()
}

// IsWorkflowControlTool reports whether a name is one of the machine's own
// controls. False before the app registers them, which is why the runtime
// exemption in the phase narrowing is the guarantee and this is the warning.
func IsWorkflowControlTool(name string) bool {
	reservedToolNamesMu.RLock()
	defer reservedToolNamesMu.RUnlock()
	return workflowControlTools[strings.TrimSpace(name)]
}
