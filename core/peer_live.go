// Peer work, in the place local background work already appears.
//
// A borrowed model runs on this machine's GPU and competes with everything the
// operator is doing on it. Until now that was invisible: a turn took longer,
// the log said nothing, and the only honest description of the experience was
// "it got slow and I don't know why". The scheduler knew — it had been queueing
// the work and counting it per caller since peer requests started taking slots —
// but nothing read that back out.
//
// So this is a READER, not a new record. Every number here comes from the
// schedulers that are actually doing the queueing, which is the point: a
// separate counter would be a second source of truth about how busy the GPU is,
// and the two would disagree the first time anything was cancelled mid-flight.
//
// It reports peers only. Local work already has its own live entries from the
// apps that started it, and duplicating those here would double every row in
// the ribbon.
package core

import (
	"fmt"
	"sort"
	"strings"
)

func init() {
	RegisterLiveProvider(peerLiveEntries)
}

// peerLivePrefix is the caller label peer work is scheduled under. Set at the
// point of acquisition (see peer_serve.go and peer_models.go); read here.
const peerLivePrefix = "peer:"

// peerLiveOrder places these after an app's own rows in the ribbon. Someone
// else's work borrowed on your hardware is context for what you are doing, not
// the thing you came to look at.
const peerLiveOrder = 90

// peerLiveEntries reports what peers are currently doing on this instance.
func peerLiveEntries() []LiveEntry {
	var out []LiveEntry
	// Both model schedulers: a deployment runs one, and asking the idle one
	// costs a nil check.
	out = append(out, peerRowsFrom(LlamacppSchedulerStats(), "running a model")...)
	out = append(out, peerRowsFrom(OllamaSchedulerStats(), "running a model")...)
	out = append(out, peerRowsFrom(ImageSchedulerStats(), "rendering an image")...)
	// Stable order so the ribbon does not reshuffle between polls — a row that
	// moves reads as a new row, and the point of this is to be glanceable.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// peerRowsFrom turns one scheduler's per-caller counts into live rows.
//
// In-flight and queued are separate rows on purpose. "A peer is using the GPU"
// and "a peer is waiting behind you" are different facts, and the second is the
// one that explains why a queue is deep without implying the machine is busy
// with someone else's work right now.
func peerRowsFrom(st OllamaSchedStats, doing string) []LiveEntry {
	var out []LiveEntry
	for caller, n := range st.Callers {
		if n <= 0 || !strings.HasPrefix(caller, peerLivePrefix) {
			continue
		}
		out = append(out, LiveEntry{
			ID:         "peerwork:" + doing + ":" + caller,
			Label:      peerRowLabel(caller, doing, n),
			Background: true, // it started without the viewer, by definition
			// Nothing here was typed by anybody: the peer's own key label and a
			// fixed description of what it is doing. Masked, this row says
			// "Peers · another user", which withholds the only thing it is for.
			PublicLabel: true,
			App:         "Peers",
			Status:      "on this machine's hardware",
			Order:       peerLiveOrder,
		})
	}
	for caller, n := range st.Queued {
		if n <= 0 || !strings.HasPrefix(caller, peerLivePrefix) {
			continue
		}
		out = append(out, LiveEntry{
			ID:          "peerqueue:" + doing + ":" + caller,
			Label:       fmt.Sprintf("%s — %d waiting to %s", peerDisplayName(caller), n, doing),
			Queued:      true,
			Background:  true,
			PublicLabel: true,
			App:         "Peers",
			Order:       peerLiveOrder,
		})
	}
	return out
}

// peerRowLabel names the peer and what it is doing, pluralized honestly.
func peerRowLabel(caller, doing string, n int) string {
	name := peerDisplayName(caller)
	if n > 1 {
		return fmt.Sprintf("%s — %s (%d at once)", name, doing, n)
	}
	return fmt.Sprintf("%s — %s", name, doing)
}

// peerDisplayName renders the caller label as the operator knows the peer.
//
// The key's LABEL, not its id: the label is what was typed when the key was
// minted precisely so a person could recognize the far side later, and a row
// reading "peer:3f2a…" answers none of the question it exists to answer.
func peerDisplayName(caller string) string {
	name := strings.TrimSpace(strings.TrimPrefix(caller, peerLivePrefix))
	if name == "" {
		return "A peer"
	}
	return "Peer " + name
}
