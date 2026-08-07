// Coalescing must not rewrite who said what.
//
// A batch keeps the FIRST message's handle, so merging across senders put one
// person's words under another's name. In a group that is not cosmetic: the
// handle is what the owner check derives from, so the owner texting first and
// somebody else texting inside the window made THEIR message owner-authoritative
// — past the premise gate, out of the live-claim scope, and stored as the
// owner's own account of themselves.
package core

import "testing"

func TestSameSpeakerComparesHandlesNotNames(t *testing.T) {
	owner := ChannelInbound{Handle: "+15550100", SenderName: "Craig"}
	// A different person calling themselves the same thing must not merge:
	// the display name is the sender's to choose.
	impostor := ChannelInbound{Handle: "+15559999", SenderName: "Craig"}
	if sameSpeaker(owner, impostor) {
		t.Error("two handles are two people, whatever they call themselves")
	}
	// The same person renaming themselves mid-thread still merges.
	renamed := ChannelInbound{Handle: "+15550100", SenderName: "craig (phone)"}
	if !sameSpeaker(owner, renamed) {
		t.Error("one handle is one person, whatever they call themselves")
	}
	// Case and whitespace differences in a handle are the same handle.
	if !sameSpeaker(owner, ChannelInbound{Handle: " +15550100 "}) {
		t.Error("a handle should compare trimmed and case-insensitively")
	}
}

// A transport that attributes nothing gives us nothing to tell people apart
// with, so the coalescer behaves as it did before this rule existed rather than
// refusing to merge anything at all.
func TestUnattributedInboundsStillMerge(t *testing.T) {
	if !sameSpeaker(ChannelInbound{}, ChannelInbound{}) {
		t.Error("with no handles there is nothing to separate, and merging is the old behaviour")
	}
	if sameSpeaker(ChannelInbound{Handle: "+15550100"}, ChannelInbound{}) {
		t.Error("a known sender and an unknown one are not the same person")
	}
}
