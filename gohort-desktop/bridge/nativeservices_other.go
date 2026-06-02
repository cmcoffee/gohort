//go:build !darwin

package bridge

import "github.com/cmcoffee/gohort/gohort-desktop/core"

// startNativeServices is a no-op off macOS — there's no iMessage relay
// (no chat.db / AppleScript). The WS tool bridge still runs and serves
// the cross-platform tools.
func startNativeServices() {}

// runTest: the iMessage relay is macOS-only.
func runTest() { core.Log("--test is macOS-only (no iMessage relay on this platform)") }
