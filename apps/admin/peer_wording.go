package admin

// The Worker LLM section's line on prompt wording borrowed from a peer.
//
// When the worker is a peer's model, that peer's tuned wording governs here
// and is fetched every few minutes (core/peer_models.go). Nothing on screen
// said so: whether it had arrived, from whom, and how old the copy was could
// be read only from the log, which made the sync untestable by looking and
// a peer gone quiet invisible.

import (
	"fmt"
	"strings"

	"github.com/cmcoffee/oddjob/core/prompts"
)

// peerWordingLine is the line for a worker whose provider is provider, or ""
// when this machine reads only its own wording (the field then hides).
func peerWordingLine(provider string) string {
	peer, isPeer := strings.CutPrefix(strings.TrimSpace(provider), "peer:")
	st := prompts.PeerPromptLayer()
	if st.Source == "" {
		if !isPeer {
			return ""
		}
		return fmt.Sprintf("None from %s yet. It is asked within a minute of being chosen, and a peer on an older build sends none.", peer)
	}
	line := fmt.Sprintf("%d block(s) of %s's wording", st.Count, st.Source)
	if st.Model != "" {
		line += ", tuned for " + st.Model
	}
	line += ", fetched " + relativeWhen(st.At) + "."
	switch {
	case !isPeer:
		line += " The worker is no longer a peer's model, so this copy is dropped within a minute."
	case peer != st.Source:
		line += fmt.Sprintf(" The worker is now %s's model; its wording replaces this within a minute.", peer)
	}
	return line
}
