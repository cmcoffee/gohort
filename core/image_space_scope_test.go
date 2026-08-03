package core

// Whose picture is image#1. The refs are POSITIONAL, so a ring shared across a
// fleet hands an agent asking for "the one you just made" whatever another
// agent made a second earlier — silently, plausibly, and wrong.

import (
	"bytes"
	"testing"
)

func spaceSession(t *testing.T, user, agent string) *ToolSession {
	t.Helper()
	return &ToolSession{Username: user, AgentID: agent}
}

func TestOneAgentsPictureIsNotAnothersImageOne(t *testing.T) {
	attachmentTestDir(t) // scopes ImageDir to a temp dir
	wren := spaceSession(t, "alice", "agent-wren")
	other := spaceSession(t, "alice", "agent-wiwee")

	if ref := RecordRecentImage(wren, []byte("WREN-PICTURE"), "wren made this"); ref != "image#1" {
		t.Fatalf("record returned %q", ref)
	}
	if ref := RecordRecentImage(other, []byte("OTHER-PICTURE"), "wiwee made this"); ref != "image#1" {
		t.Fatalf("record returned %q", ref)
	}

	got, ok := ResolveRecentImage(wren, "image#1")
	if !ok {
		t.Fatal("wren should still see its own picture")
	}
	if !bytes.Equal(got, []byte("WREN-PICTURE")) {
		t.Errorf("image#1 for wren resolved to another agent's picture: %q", got)
	}
	got, ok = ResolveRecentImage(other, "image#1")
	if !ok || !bytes.Equal(got, []byte("OTHER-PICTURE")) {
		t.Errorf("image#1 for the other agent resolved to %q", got)
	}
	// Each ring holds only its own.
	if n := len(RecentImages(wren)); n != 1 {
		t.Errorf("wren's ring holds %d, want only its own picture", n)
	}
}

func TestTheSameAgentKeepsOneRingAcrossItsSurfaces(t *testing.T) {
	// Web chat and a phone conversation are the same agent, and "edit the one
	// you just made" has to work across them — including from the wake turn,
	// which runs under a different session id than the task that made it.
	attachmentTestDir(t)
	made := &ToolSession{Username: "alice", AgentID: "agent-wren", ChatSessionID: "web-session"}
	woken := &ToolSession{Username: "alice", AgentID: "agent-wren", ChatSessionID: "scheduled:chan:xyz"}

	RecordRecentImage(made, []byte("THE-EDIT"), "edited")
	got, ok := ResolveRecentImage(woken, "image#1")
	if !ok || !bytes.Equal(got, []byte("THE-EDIT")) {
		t.Errorf("the same agent must reach its own picture from any session, got %q ok=%v", got, ok)
	}
}

func TestASessionWithNoAgentGetsItsOwnRing(t *testing.T) {
	attachmentTestDir(t)
	anon := &ToolSession{Username: "alice"}
	named := spaceSession(t, "alice", "agent-wren")
	RecordRecentImage(named, []byte("AGENT-PICTURE"), "")
	RecordRecentImage(anon, []byte("ANON-PICTURE"), "")

	got, _ := ResolveRecentImage(anon, "image#1")
	if !bytes.Equal(got, []byte("ANON-PICTURE")) {
		t.Errorf("an agent-less session must not read an agent's ring, got %q", got)
	}
	if n := len(RecentImages(named)); n != 1 {
		t.Errorf("the agent's ring picked up an unrelated image (%d entries)", n)
	}
}
