package account

// The forwarding chooser has to say what it can actually do. An option that
// silently does nothing is worse than one that is not offered: you turn it on,
// believe you will be told, and are not. This regressed once already, when the
// chooser moved from a modal that reported capability to a plain select that
// did not.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestForwardingOptionsSayWhatTheyCanDo(t *testing.T) {
	saved := NoticePhoneReady
	t.Cleanup(func() { NoticePhoneReady = saved })
	NoticePhoneReady = func(string) bool { return false }

	byValue := func(user string) map[string]string {
		out := map[string]string{}
		for _, o := range notifyForwardOptions(user) {
			out[o.Value] = o.Label
		}
		return out
	}

	// The one that surprises people: there is no separate address on an
	// account, so a username that is not an email can never receive one.
	opts := byValue("craig")
	if !strings.Contains(opts["email"], "username is not an email") {
		t.Errorf("email does not explain why it cannot reach this account: %q", opts["email"])
	}
	if !strings.Contains(opts["phone"], "unavailable") {
		t.Errorf("phone does not say the bridge is missing: %q", opts["phone"])
	}
	if !strings.Contains(opts["both"], "neither") {
		t.Errorf("both does not say that nothing is set up: %q", opts["both"])
	}
	// Nowhere is always available: it is the absence of a transport.
	if strings.Contains(opts["off"], "unavailable") {
		t.Errorf("the off option claims to be unavailable: %q", opts["off"])
	}

	// With a bridge, phone stops apologizing and "both" names only the half
	// that is missing, so the reader knows which one to go and fix.
	NoticePhoneReady = func(string) bool { return true }
	opts = byValue("craig")
	if strings.Contains(opts["phone"], "unavailable") {
		t.Errorf("phone is available and still says otherwise: %q", opts["phone"])
	}
	if !strings.Contains(opts["both"], "email half") {
		t.Errorf("both does not name which half is missing: %q", opts["both"])
	}

	// Every option is still OFFERED whatever the state: a stored choice has to
	// render as itself, and a setting that vanishes when its transport goes
	// down is one nobody can reason about.
	if len(notifyForwardOptions("craig")) != 4 {
		t.Error("an unavailable option was dropped from the list")
	}
}
