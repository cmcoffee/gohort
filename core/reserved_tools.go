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
package core

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
