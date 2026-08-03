// Delivering a finished background result back to the conversation that asked.
// The wake rides the scheduled-fire path, which APPENDS to a stored session and
// stops — right for a recurring task posting into a web thread, and useless
// when the conversation lives on a phone.
package orchestrate

import "testing"

func TestAPerContactThreadResolvesToItsConversation(t *testing.T) {
	// "chan:<address>" carries the recipient in the session id.
	chatID, _, ok := channelTargetForWake("alice", "agent-1", "chan:any;+;chat872212368359368118")
	if !ok {
		t.Fatal("a per-contact channel thread must resolve to a recipient")
	}
	if chatID != "any;+;chat872212368359368118" {
		t.Errorf("chat id = %q, want the address from the session id", chatID)
	}
}

func TestAWebSessionIsNotSentAnywhere(t *testing.T) {
	// A plain web session needs no send — the user is looking at the thread, and
	// a message to a conversation that isn't one would be a misroute.
	if _, _, ok := channelTargetForWake("alice", "agent-1", "8cf98a5d-ed40-483a-bcea-e3b6f3c2fdf0"); ok {
		t.Error("a web session must not resolve to a messaging recipient")
	}
	if _, _, ok := channelTargetForWake("alice", "agent-1", ""); ok {
		t.Error("an empty session resolves to nothing")
	}
}

func TestACortexHomeWithNoSingleChannelStaysPut(t *testing.T) {
	// The cortex home IS the channel thread for a ONE-channel agent. With none
	// bound — or several — there is no single right recipient, and guessing
	// would send a private result to the wrong conversation.
	if _, _, ok := channelTargetForWake("alice", "agent-with-no-channels", cortexSessionID("agent-with-no-channels")); ok {
		t.Error("an agent with no bound channel has nowhere to deliver")
	}
}
