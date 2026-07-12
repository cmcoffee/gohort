//go:build darwin

package bridge

import (
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/gohort/gohort-desktop/macos/imsg"

	// macOS-only tools — imported here so they register into the catalog
	// and never link into a non-Mac build.
	_ "github.com/cmcoffee/gohort/gohort-desktop/macos/contacts" // contacts.lookup
)

// startNativeServices supervises the iMessage relay: it starts the relay once
// the sidecar has a server URL + key AND the service is enabled, and stops it
// LIVE when a server-side disable arrives (a messaging_bridge connector
// unapproved/deleted) — no daemon restart needed. It waits (rather than giving
// up) so the daemon can be running before the user configures it; config +
// enable/disable show up live on the 5s supervisor tick.
func startNativeServices() {
	// Touch chat.db immediately (even before configured) so macOS lists
	// this app under Full Disk Access, ready to toggle on — the user
	// doesn't have to add it with the "+" button.
	imsg.ProbeChatDB()

	go func() {
		var stop, done chan struct{} // non-nil while the relay goroutine is live
		for {
			c := core.ReadBridgeConfig()
			// core.BridgeAPIKey() is the single live resolver both this relay
			// and the WS bridge share — a rotated / auto-provisioned key is
			// picked up without restarting.
			configured := c.ServerURL != "" && core.BridgeAPIKey() != ""
			enabled, pollOverride, managed := core.BridgeServiceEnabled("imessage")
			// Default-on when the server never pushed state (managed=false) —
			// preserves the historic behavior (relay runs whenever the daemon
			// is configured). An explicit disable turns it off.
			shouldRun := configured && !(managed && !enabled)

			switch {
			case shouldRun && stop == nil:
				poll := c.PollSecs
				if pollOverride > 0 {
					poll = pollOverride
				}
				cfg := imsg.Config{
					ServerURL: c.ServerURL, // bare origin; imsg appends /phantom
					APIKey:    core.BridgeAPIKey,
					PollSecs:  poll,
				}
				stop, done = make(chan struct{}), make(chan struct{})
				go func(stop, done chan struct{}) {
					defer close(done)
					imsg.Run(cfg, false, false, stop)
				}(stop, done)
				core.Log("[bridge] iMessage relay started")
			case !shouldRun && stop != nil:
				close(stop)
				<-done // let the relay fully exit before a possible restart
				stop, done = nil, nil
				core.Log("[bridge] iMessage relay stopped (server-disabled)")
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

// runTest is the --test path on macOS: print recent Messages to verify
// Full Disk Access without contacting the server.
func runTest() { imsg.RunTest("~/Library/Messages/chat.db") }
