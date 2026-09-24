package bridge

import (
	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/gohort/gohort-desktop/wsbridge"
)

// daemonApprover gates server-initiated tool calls. The agent has no
// Wails window, so it asks via the platform prompt (a native NSAlert on
// macOS; deny-by-default elsewhere — see promptApproval in platform_*.go).
// Before prompting it consults the shared BridgeConfig sidecar — the same
// list the viewer's "Manage permissions" UI edits — so AutoApprove and
// per-tool "Always allow" actually take effect across the two processes.
// Satisfies wsbridge.Approver.
type daemonApprover struct{}

func (daemonApprover) RequestApprovalBlocking(id, name string, args map[string]any) bool {
	// Installing a capability or enabling a relay always asks: the skip
	// toggle and "Always allow" cover calls to tools already here, not new
	// code the server pushes (see wsbridge.IsInstallConsent).
	if wsbridge.IsInstallConsent(name) {
		allow, _ := promptApproval(name, args)
		return allow
	}
	bc := core.ReadBridgeConfig()
	if bc.AutoApprove {
		return true
	}
	for _, t := range bc.AllowedTools {
		if t == name {
			return true // user previously chose "Always allow" for this tool
		}
	}
	allow, always := promptApproval(name, args)
	if allow && always {
		// Persist the grant to the shared sidecar so future calls skip the
		// prompt and the viewer's Manage permissions UI lists it. Re-read
		// to avoid clobbering a concurrent edit; no-op if already present.
		cur := core.ReadBridgeConfig()
		present := false
		for _, t := range cur.AllowedTools {
			if t == name {
				present = true
				break
			}
		}
		if !present {
			cur.AllowedTools = append(cur.AllowedTools, name)
			_ = core.WriteBridgeConfig(cur)
		}
	}
	return allow
}
