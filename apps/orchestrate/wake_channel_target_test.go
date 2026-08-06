// Where a finished background task gets SENT.
//
// The reported failure: an agent on a channel says it is working on something,
// and the result never arrives on the phone. It was resolving the recipient
// AFTER the fact, from the session id — and the ordinary way to put an agent on
// a messaging service leaves nothing there to resolve. A whole-service channel
// binds with an empty Address (it matches every thread on the service), and a
// cortex agent collapses every contact into one home thread, so the id names
// the agent rather than the person. No recipient, no delivery, silently, every
// time.
package orchestrate

import "testing"

// The captured origin is what makes the failing configuration work.
func TestWakeTargetPrefersTheCapturedOrigin(t *testing.T) {
	p := orchUpdatePayload{
		Username:      "craig",
		AgentID:       "wiwee",
		SessionID:     cortexSessionID("wiwee"), // the collapsed home: derivation cannot reach a person from here
		ChannelChatID: "iMessage;-;+15551234567",
	}
	chatID, handle, ok := wakeChannelTarget(p)
	if !ok {
		t.Fatal("a captured origin must resolve a target even when the session id cannot")
	}
	if chatID != "iMessage;-;+15551234567" || handle != "" {
		t.Errorf("origin should pass through verbatim, got chat=%q handle=%q", chatID, handle)
	}
}

// A handle with no chat id is a real case — the transport tells them apart, so
// they must not be fused on the way through.
func TestWakeTargetCarriesAHandleOnlyOrigin(t *testing.T) {
	chatID, handle, ok := wakeChannelTarget(orchUpdatePayload{
		Username: "craig", AgentID: "wiwee",
		SessionID:     cortexSessionID("wiwee"),
		ChannelHandle: "+15551234567",
	})
	if !ok {
		t.Fatal("a handle-only origin is still a target")
	}
	if chatID != "" || handle != "+15551234567" {
		t.Errorf("handle must stay a handle, got chat=%q handle=%q", chatID, handle)
	}
}

// A task that detached before origins were carried still has to work, so
// derivation remains the fallback rather than being replaced.
func TestWakeTargetFallsBackToDerivationWhenNoOriginWasCaptured(t *testing.T) {
	// "chan:<address>" carries its own recipient, which is the case derivation
	// was always able to handle.
	chatID, _, ok := wakeChannelTarget(orchUpdatePayload{
		Username: "craig", AgentID: "wiwee", SessionID: "chan:+15559876543",
	})
	if !ok {
		t.Fatal("a per-contact session id should still derive its recipient")
	}
	if chatID != "+15559876543" {
		t.Errorf("derived recipient = %q, want the address in the session id", chatID)
	}
}

// A web session is not a channel, and must not be turned into one by a blank
// origin reading as present.
func TestWakeTargetRefusesAWebSession(t *testing.T) {
	if _, _, ok := wakeChannelTarget(orchUpdatePayload{
		Username: "craig", AgentID: "wiwee", SessionID: "sess-abc123",
	}); ok {
		t.Error("a web session has no channel to deliver to")
	}
}
