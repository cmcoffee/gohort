// Serving JUST the new observation cards to the live poll.
//
// Every open thread re-asks for itself every six seconds so a card the server
// posted (a cortex observation, a scheduled report, a monitor wake) shows up
// while the user is looking at the thread rather than only after a reload. That
// poll was fetching the WHOLE served tail each time and throwing all of it away
// but the handful of cards it had not seen — the client already dedupes by card
// id. On a cortex, which is append-only and never ends, that is the entire
// transcript plus every persisted tool call's arguments and output, serialized,
// transferred, and JSON-parsed ten times a minute for as long as the tab is
// open. The thread does not have to be pathological for that to be the dominant
// cost of having it on screen; it only has to be long.
//
// So the poll asks for what it actually wants. `cards=1` returns observation
// cards only — no chat turns, no UI blocks, no plan snapshots — and `since`
// narrows that to cards newer than the most recent one the client already has.
// The steady state is an empty list, which is the honest shape of "nothing has
// happened in the last six seconds".
//
// Display-only, like the tail trim next door: nothing here touches storage or
// what a turn is given as context. And deliberately NOT a read receipt — the
// full load marks the session seen because opening a thread means you looked at
// it, but a background poll firing on a timer is not a person reading, and it
// was writing that claim to the database every six seconds per open tab.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// cardsPayload is what the poll gets back.
//
// The tag matches ChatSession.Messages EXACTLY — capital M, and no omitempty.
// The client reads this array through the same configurable field name it uses
// for a full load (cfg.messages_field, default "Messages"), so a lowercase key
// here would read as a poll that returned nothing, forever, silently. And an
// omitted empty array would make the steady state — no new cards — arrive as a
// missing field rather than an empty one.
type cardsPayload struct {
	Messages []ChatMessage `json:"Messages"`
}

// observationCardsSince returns the report cards created strictly after `since`,
// in stored order.
//
// An empty or unparseable `since` means the client has nothing yet and gets the
// whole (already tail-bounded) set of cards — the same thing the full load would
// have handed it, minus everything it was going to discard.
//
// STRICTLY after, and the client echoes back the exact timestamp string it was
// served, so the boundary card is never re-sent to the same client. Two cards
// stamped the same instant would both come back on the next poll; the client
// dedupes by card key, so that costs a comparison rather than a duplicate.
func observationCardsSince(msgs []ChatMessage, since string) []ChatMessage {
	var after time.Time
	if s := strings.TrimSpace(since); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			after = t
		}
	}
	out := make([]ChatMessage, 0, 4)
	for _, m := range msgs {
		if strings.TrimSpace(m.ReportFrom) == "" {
			continue // observations are ReportFrom cards; chat turns ride SSE/replay
		}
		if !after.IsZero() && !m.Created.After(after) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// serveObservationCards writes the poll's response.
func serveObservationCards(w http.ResponseWriter, msgs []ChatMessage, since string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cardsPayload{Messages: observationCardsSince(msgs, since)})
}
