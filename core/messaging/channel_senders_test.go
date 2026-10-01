package messaging

import "testing"

// A channel answers the owner always and anyone else as its Senders says,
// matched on the handle as people write it; an unknown setting answers the
// owner only.
func TestAChannelAnswersWhoItSays(t *testing.T) {
	open := Channel{}
	if !open.Answers(false, "+15550100") {
		t.Error("an open channel turned someone away")
	}
	listed := Channel{Senders: SendersListed, AllowedHandles: []string{"(555) 010-0123", "Pat@Example.com"}}
	for _, h := range []string{"+15550100123", "555-010-0123", "pat@example.com"} {
		if !listed.Answers(false, h) {
			t.Errorf("%s is listed but was not answered", h)
		}
	}
	for _, h := range []string{"+15550100124", "0100123", "", "other@example.com"} {
		if listed.Answers(false, h) {
			t.Errorf("%q is not listed but was answered", h)
		}
	}
	for _, c := range []Channel{{Senders: SendersOwner}, {Senders: "ownr"}} {
		if c.Answers(false, "+15550100") || !c.Answers(true, "") {
			t.Errorf("%q: answers the owner and only the owner", c.Senders)
		}
	}
}
