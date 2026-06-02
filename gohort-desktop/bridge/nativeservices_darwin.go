//go:build darwin

package bridge

import (
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/gohort/gohort-desktop/macos/imsg"

	// macOS-only tools — imported here so they register into the catalog
	// and never link into a non-Mac build.
	_ "github.com/cmcoffee/gohort/gohort-desktop/macos/contacts"   // contacts.lookup
	_ "github.com/cmcoffee/gohort/gohort-desktop/macos/screenshot" // screenshot.capture
)

// startNativeServices launches the iMessage relay once the sidecar has
// a server URL + API key. It waits (rather than giving up) so the agent
// can be running before the user configures it in the viewer — config
// shows up live and the relay starts. imsg.Run blocks, so this owns a
// goroutine for the process lifetime.
func startNativeServices() {
	// Touch chat.db immediately (even before configured) so macOS lists
	// this app under Full Disk Access, ready to toggle on — the user
	// doesn't have to add it with the "+" button.
	imsg.ProbeChatDB()

	go func() {
		for {
			c := core.ReadBridgeConfig()
			if c.ServerURL != "" && c.APIKey != "" {
				imsg.Run(imsg.Config{
					ServerURL: c.ServerURL, // bare origin; imsg appends /phantom
					APIKey:    c.APIKey,
					PollSecs:  c.PollSecs,
				}, false, false)
				return
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

// runTest is the --test path on macOS: print recent Messages to verify
// Full Disk Access without contacting the server.
func runTest() { imsg.RunTest("~/Library/Messages/chat.db") }
