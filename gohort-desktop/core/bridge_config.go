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
