// Server-pushed bridge state — which BUILT-IN messaging relays (iMessage, …)
// the server has enabled on this device, and their poll overrides.
//
// A separate file from bridge-config.json on purpose: the VIEWER owns
// bridge-config.json (server URL, key, consent lists) and rewrites it wholesale
// on save, so the daemon writing into it would race and clobber. This file is
// owned by the DAEMON (written when it applies a server install frame), same
// cross-process split as mcp.json / commands.json for other pushed
// capabilities. startNativeServices reads it to decide whether to run a relay.

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const BRIDGE_STATE_NAME = "bridges.json"

// BridgeState records the server-enabled built-in bridges on this device.
type BridgeState struct {
	Services map[string]BridgeServiceState `json:"services,omitempty"`
}

// BridgeServiceState is the per-service enabled flag + poll override.
type BridgeServiceState struct {
	Enabled  bool `json:"enabled"`
	PollSecs int  `json:"poll_secs,omitempty"`
}

func bridge_state_path() (string, error) {
	dir, err := settings_dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, BRIDGE_STATE_NAME), nil
}

// ReadBridgeState returns the persisted state, tolerating a missing/invalid file.
func ReadBridgeState() BridgeState {
	var st BridgeState
	if path, err := bridge_state_path(); err == nil {
		if b, rerr := os.ReadFile(path); rerr == nil {
			_ = json.Unmarshal(b, &st)
		}
	}
	if st.Services == nil {
		st.Services = map[string]BridgeServiceState{}
	}
	return st
}

func writeBridgeState(st BridgeState) error {
	path, err := bridge_state_path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// SetBridgeService records a server-enabled bridge + its poll interval.
func SetBridgeService(service string, pollSecs int) error {
	st := ReadBridgeState()
	st.Services[service] = BridgeServiceState{Enabled: true, PollSecs: pollSecs}
	return writeBridgeState(st)
}

// RemoveBridgeService marks a service explicitly disabled — kept as an off
// record (not deleted) so startNativeServices honors the disable instead of
// falling back to its default-on behavior.
func RemoveBridgeService(service string) error {
	st := ReadBridgeState()
	st.Services[service] = BridgeServiceState{Enabled: false}
	return writeBridgeState(st)
}

// BridgeServiceEnabled reports whether a built-in bridge should run and its poll
// override. `managed` is false when the server has never pushed state for the
// service — the caller then applies its own default (iMessage historically
// auto-starts when the daemon is configured, so default-on preserves that).
func BridgeServiceEnabled(service string) (enabled bool, pollSecs int, managed bool) {
	s, ok := ReadBridgeState().Services[service]
	if !ok {
		return false, 0, false
	}
	return s.Enabled, s.PollSecs, true
}
