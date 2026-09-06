package core

import (
	"strings"
)

// LatestUserContent returns the content of the most recent user message, or ""
// when there is none.
func LatestUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// StripDeliveryMarkers removes [ATTACH: …] markers from a reply. Used when the
// files they name do not exist: the marker is the claim, so removing it is what
// stops the claim from being delivered or persisted.
func StripDeliveryMarkers(s string) string {
	return strings.TrimSpace(deliveryMarkerRe.ReplaceAllString(s, ""))
}
