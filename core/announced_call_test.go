package core

import "testing"

// endsWithCallAnnouncement backs the announced-call correction guard: a reply
// that ENDS on a colon introducing a tool call that never followed (the
// Builder "Here's the `update_agent` call to implement these changes:" settle,
// 2026-07-24) must re-prompt; complete replies and legitimate turn-ending
// colons (asking the USER to paste something) must not.
func TestEndsWithCallAnnouncement(t *testing.T) {
	fire := []string{
		"Here's the `update_agent` call to implement these changes:",
		"I will perform the following updates:\n\nHere's the `update_agent` call to implement these changes:",
		"Now I'll remove the duplicate action with this tool call:",
		"Let me fix that. Calling reply_to_comment with the corrected args:",
		"First I'll update the moltbook toolbox:",
		// The live miss (2026-07-28): announces work, ends on a colon, names
		// no tool and contains no snake_case — the old call-word requirement
		// passed it through and the user watched the turn end on a promise.
		"Let me dig up that benchmark article with actual token/s numbers:",
		"Here's the plan:",
		"Now I'll pull the numbers:",
	}
	noFire := []string{
		"Paste the error message here:",
		"The tool is now fixed and the agent will use it correctly.",
		"I'll try to nail the house next time.",
		"Here are your options:\n1. Remove the toolbox action\n2. Remove the standalone reply handler",
		"That's the plan, basically:", // "basically" must not match \bcall\b
		// Colons that hand the next move to the USER end a turn legitimately;
		// only the agent committing ITSELF and then stopping is broken.
		"Send me the link and I'll take a look:",
		"Reply with one of these:",
		"",
	}
	for _, c := range fire {
		if !endsWithCallAnnouncement(c) {
			t.Errorf("must fire on announce-then-stop: %q", c)
		}
	}
	for _, c := range noFire {
		if endsWithCallAnnouncement(c) {
			t.Errorf("must NOT fire on complete/legitimate reply: %q", c)
		}
	}
}
