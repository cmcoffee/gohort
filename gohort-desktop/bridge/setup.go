package bridge

import (
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/snugforge/nfo"
)

// Setup is the optional CLI configuration wizard for the bridge agent.
// Normally the user configures in the viewer (Gohort.app), which writes
// the same sidecar — this is just a headless alternative. It stores the
// BARE server origin (the /phantom prefix is appended by the relay; the
// WS bridge needs the prefix-less origin).
func Setup() {
	cur := core.ReadBridgeConfig()
	serverURL := cur.ServerURL
	apiKey := cur.APIKey
	pollSecs := cur.PollSecs
	if pollSecs == 0 {
		pollSecs = 5 // matches the floor; snappier message pickup, negligible cost
	}

	menu := nfo.NewOptions("--- Gohort Bridge Configuration ---", "(selection or 'q' to save & exit)", 'q')
	menu.StringVar(&serverURL, "Gohort Server URL", serverURL, "Base origin of the gohort server (e.g. https://gohort.example.com).")
	menu.SecretVar(&apiKey, "API Key", apiKey, "Generate one in the Phantom web UI under API Keys.")
	menu.IntVar(&pollSecs, "Poll Interval (seconds)", pollSecs, "How often to check for new messages.", 5, 300)
	menu.Select(false)

	// Normalize to a bare origin: drop trailing slash and any /phantom suffix.
	serverURL = strings.TrimRight(serverURL, "/")
	serverURL = strings.TrimSuffix(serverURL, "/phantom")
	serverURL = strings.TrimRight(serverURL, "/")

	if err := core.WriteBridgeConfig(core.BridgeConfig{
		ServerURL: serverURL,
		APIKey:    apiKey,
		PollSecs:  pollSecs,
	}); err != nil {
		nfo.Fatal("cannot save bridge config: %v", err)
	}
	nfo.Log("Configuration saved.")
	nfo.Log("Next: install Gohort-Bridge.app as a login item to start it at boot.")
}
