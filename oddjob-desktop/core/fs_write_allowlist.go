// Sibling write-allowlist for filesystem write tools. Deliberately
// SEPARATE from the read allowlist — writing is more dangerous than
// reading, so a folder approved for reads is NOT automatically writable.
// The user approves writes per-folder on their own track (the bridge
// persists approvals to bridge-config.json's write_roots).

package core

import "sync"

var (
	fs_write_mu    sync.RWMutex
	fs_write_state []string

	fsWriteConsentMu sync.RWMutex
	fsWriteConsent   func(abs string) bool
)

// SetAllowedWriteRoots replaces the in-memory write allowlist (the bridge
// loads it from the sidecar at startup and after each approval).
func SetAllowedWriteRoots(paths []string) {
	clean := normalize_roots(paths)
	fs_write_mu.Lock()
	fs_write_state = clean
	fs_write_mu.Unlock()
}

// AllowedWriteRoots returns a copy of the live write allowlist.
func AllowedWriteRoots() []string {
	fs_write_mu.RLock()
	defer fs_write_mu.RUnlock()
	out := make([]string, len(fs_write_state))
	copy(out, fs_write_state)
	return out
}

// PathWriteAllowed reports whether abs is under an approved write root.
func PathWriteAllowed(abs string) bool { return pathUnderAny(abs, AllowedWriteRoots()) }

// RegisterFSWriteConsent installs the just-in-time write-consent handler
// (the bridge's native "Allow WRITE to <folder>?" prompt). No handler →
// writes outside the allowlist are denied.
func RegisterFSWriteConsent(fn func(abs string) bool) {
	fsWriteConsentMu.Lock()
	fsWriteConsent = fn
	fsWriteConsentMu.Unlock()
}

// PathWriteAllowedOrConsent is the write gate: allow if abs is under an
// approved write root, otherwise ask the consent handler (which persists
// its approval, so the next write under that folder passes).
func PathWriteAllowedOrConsent(abs string) bool {
	if PathWriteAllowed(abs) {
		return true
	}
	fsWriteConsentMu.RLock()
	fn := fsWriteConsent
	fsWriteConsentMu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(abs)
}
