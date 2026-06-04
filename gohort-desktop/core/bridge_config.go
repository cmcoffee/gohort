// Cross-process config channel between the two apps.
//
// Gohort.app (the viewer) and Gohort-Bridge.app (the always-on agent)
// are separate processes, so they can't share one kvlite/bolt file
// (exclusive lock). Instead the viewer's configure page writes the
// bridge's settings to a small JSON sidecar, and the bridge reads (and
// re-reads) it. No lock contention; the bridge picks up changes live.
//
// The API key lives here in plaintext (0600, in the per-user app
// support dir). On a single-user local machine that's an acceptable
// trade for not having two processes fight over one encrypted store —
// kvlite's encryption is hardware-locked obfuscation, not secret
// storage, anyway.

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const BRIDGE_CONFIG_NAME = "bridge-config.json"

// BridgeConfig is everything the bridge agent needs to run: where the
// gohort server is, the API key to authenticate with, and the chat.db
// poll interval. The viewer owns writing it; the bridge owns reading it.
type BridgeConfig struct {
	ServerURL string `json:"server_url"` // bare origin, e.g. https://gohort.example.com
	APIKey    string `json:"api_key"`
	PollSecs  int    `json:"poll_secs,omitempty"`
	// AllowedRoots are folders a human has approved the filesystem tools
	// to read (just-in-time consent persists here). Empty by default —
	// nothing is readable until someone says yes.
	AllowedRoots []string `json:"allowed_roots,omitempty"`
	// WriteRoots are folders approved for WRITING — a separate, stricter
	// list than AllowedRoots (read access never implies write access).
	WriteRoots []string `json:"write_roots,omitempty"`
	// AllowedTools are tools the user clicked "Always allow" on — the
	// daemon's approver auto-approves them without prompting. Stored here
	// (lock-free sidecar) so the viewer's "Manage permissions" UI and the
	// daemon's approval gate operate on ONE list across the two processes
	// — same cross-process pattern as AllowedRoots for filesystem consent.
	// Before the bridge consolidation this lived in the viewer's settings
	// store, which the daemon couldn't see — so managing it did nothing.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// AutoApprove, when true, makes the daemon approve every server-
	// initiated tool call without prompting (the master "skip approvals"
	// toggle the viewer's Preferences flips).
	AutoApprove bool `json:"auto_approve,omitempty"`
}

func bridge_config_path() (string, error) {
	dir, err := settings_dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, BRIDGE_CONFIG_NAME), nil
}

// WriteBridgeConfig persists the bridge config for the agent to read.
// Called by the viewer when the user saves settings.
func WriteBridgeConfig(c BridgeConfig) error {
	path, err := bridge_config_path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// BridgeAPIKey is THE single source of truth for which key the bridge
// authenticates with — both the WS tool bridge and the iMessage relay
// (and the menu-bar status) must call this, never read a key directly.
// Resolution: the viewer-provisioned sidecar (api_key.txt) wins; the
// manually-set bridge-config key is the headless / no-viewer fallback.
//
// This exists because the two key consumers diverged once: the relay and
// the WS bridge each re-read a key from a different place, so one
// authenticated while the other 401'd on a stale key. One resolver, one
// answer — don't reintroduce a second read path.
func BridgeAPIKey() string {
	if k := ReadAPIKeySidecar(); k != "" {
		return k
	}
	return ReadBridgeConfig().APIKey
}

// ReadBridgeConfig returns the persisted bridge config, or a zero value
// if it hasn't been written yet (bridge then idles until configured).
// Cheap enough to call on every reconnect so config edits take effect
// without restarting the agent.
func ReadBridgeConfig() BridgeConfig {
	path, err := bridge_config_path()
	if err != nil {
		return BridgeConfig{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return BridgeConfig{}
	}
	var c BridgeConfig
	if json.Unmarshal(b, &c) != nil {
		return BridgeConfig{}
	}
	return c
}
