package bridge

import (
	"path/filepath"

	"github.com/cmcoffee/gohort/gohort-desktop/core"

	// Register the cross-platform filesystem.* tools so the agent serves
	// them over the WS bridge. ONLY the agent has any file-read
	// capability — the viewer registers no filesystem tools.
	_ "github.com/cmcoffee/gohort/gohort-desktop/tools/filesystem"
)

// initFSConsent loads the folders a human has already approved and wires
// the just-in-time consent handler. Nothing is readable by default —
// there are NO seeded roots; the agent starts from the approvals in the
// sidecar only. The first time the server's agent touches a new folder,
// promptFolderConsent asks the user; on "yes" the folder is remembered.
func initFSConsent() {
	cur := core.ReadBridgeConfig()
	core.SetAllowedReadRoots(cur.AllowedRoots) // approvals only, no defaults
	core.SetAllowedWriteRoots(cur.WriteRoots)

	core.RegisterFSConsent(func(abs string) bool {
		folder := filepath.Dir(abs)
		if !promptFolderConsent(folder) {
			core.Log("[bridge] denied read access to %s", folder)
			return false
		}
		bc := core.ReadBridgeConfig()
		bc.AllowedRoots = append(bc.AllowedRoots, folder)
		if err := core.WriteBridgeConfig(bc); err != nil {
			core.Warn("[bridge] persist read approval failed: %v", err)
		}
		core.SetAllowedReadRoots(bc.AllowedRoots)
		core.Log("[bridge] approved read access to %s", folder)
		return true
	})

	core.RegisterFSWriteConsent(func(abs string) bool {
		folder := filepath.Dir(abs)
		if !promptWriteConsent(folder) {
			core.Log("[bridge] denied write access to %s", folder)
			return false
		}
		bc := core.ReadBridgeConfig()
		bc.WriteRoots = append(bc.WriteRoots, folder)
		if err := core.WriteBridgeConfig(bc); err != nil {
			core.Warn("[bridge] persist write approval failed: %v", err)
		}
		core.SetAllowedWriteRoots(bc.WriteRoots)
		core.Log("[bridge] approved WRITE access to %s", folder)
		return true
	})
}
