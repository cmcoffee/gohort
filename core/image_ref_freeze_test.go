package core

// image#N is a POSITION, and every save renumbers the ring. The gap this covers
// is between the model CHOOSING a ref and the call USING it: a render saved in
// between slides into image#1 and pushes everything the model was looking at
// down one, so the call lands on a picture nobody asked about. Delivered, that
// is a reply carrying two pictures — the right one, and one from an earlier
// request entirely.

import (
	"bytes"
	"testing"
)

// freezeSession builds a session with its own ring, and a snapshot already
// taken — the state a tool round starts in.
func freezeSession(t *testing.T, user, agent string) *ToolSession {
	t.Helper()
	return &ToolSession{Username: user, AgentID: agent}
}

func TestPositionalRefSurvivesASaveInTheSameRound(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")

	RecordRecentImage(sess, []byte("THE-PHOTO-SHE-SENT"), "a photo", ImageFromUser)
	// The model reads the round's tools with this ring in front of it.
	SnapshotImageRefs(sess)

	args := map[string]any{"path": "image#1"}
	// A sibling render lands mid-round and takes over image#1.
	RecordRecentImage(sess, []byte("A-RENDER"), "generated: something else", ImageFromGenerated)

	if n := FreezeImageRefs(sess, args); n != 1 {
		t.Fatalf("froze %d refs, want 1", n)
	}
	got, ok := ResolveRecentImage(sess, args["path"].(string))
	if !ok {
		t.Fatalf("frozen ref %q no longer resolves", args["path"])
	}
	if !bytes.Equal(got, []byte("THE-PHOTO-SHE-SENT")) {
		t.Errorf("image#1 delivered the render that arrived after the model chose it: %q", got)
	}
	// Live resolution is what the freeze exists to bypass — pinned here so the
	// test fails if the ring ever stops renumbering and this stops proving
	// anything.
	live, _ := ResolveRecentImage(sess, "image#1")
	if bytes.Equal(live, []byte("THE-PHOTO-SHE-SENT")) {
		t.Fatal("the ring did not renumber; this test no longer covers the failure")
	}
}

func TestFreezeRewritesEveryRefInAList(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("OLDEST"), "a photo", ImageFromUser)
	RecordRecentImage(sess, []byte("MIDDLE"), "another photo", ImageFromUser)
	SnapshotImageRefs(sess)

	args := map[string]any{"images": []any{"image#1", "image#2"}}
	RecordRecentImage(sess, []byte("LATE-RENDER"), "generated", ImageFromGenerated)

	if n := FreezeImageRefs(sess, args); n != 2 {
		t.Fatalf("froze %d refs, want 2", n)
	}
	list := args["images"].([]any)
	first, _ := ResolveRecentImage(sess, list[0].(string))
	second, _ := ResolveRecentImage(sess, list[1].(string))
	if !bytes.Equal(first, []byte("MIDDLE")) || !bytes.Equal(second, []byte("OLDEST")) {
		t.Errorf("list resolved to %q and %q, want MIDDLE and OLDEST", first, second)
	}
}

func TestFreezeLeavesProseAlone(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)

	// A prompt MENTIONING a ref is the model describing a picture, not
	// addressing one. Rewriting inside it would put a machine id in a sentence
	// a user reads.
	prompt := "make it look like image#1 but at night"
	args := map[string]any{"prompt": prompt}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote %d refs inside prose", n)
	}
	if args["prompt"] != prompt {
		t.Errorf("prompt was rewritten to %q", args["prompt"])
	}
}

func TestFreezeLeavesStableAndKeptRefsAlone(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)
	all := RecentImages(sess)
	if len(all) != 1 || all[0].ID == "" {
		t.Fatalf("expected one picture with a stable id, got %+v", all)
	}

	args := map[string]any{"a": all[0].ID, "b": "image#brand_mark", "c": "not a ref"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote %d refs that were already durable", n)
	}
	if args["a"] != all[0].ID || args["b"] != "image#brand_mark" || args["c"] != "not a ref" {
		t.Errorf("args were altered: %+v", args)
	}
}

func TestWithoutASnapshotPositionsResolveLive(t *testing.T) {
	// A caller that never wires the hook keeps the old behavior rather than
	// silently resolving against an empty snapshot and failing to find
	// anything.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)

	args := map[string]any{"path": "image#1"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Fatalf("froze %d refs with no snapshot taken", n)
	}
	got, ok := ResolveRecentImage(sess, args["path"].(string))
	if !ok || !bytes.Equal(got, []byte("A-PHOTO")) {
		t.Errorf("unfrozen ref stopped resolving: %q ok=%v", got, ok)
	}
}

func TestSnapshotIsRetakenEachRound(t *testing.T) {
	// The freeze must not pin a ref FOREVER: once the model has seen the
	// results naming the new picture, image#1 legitimately means that one.
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("FIRST"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)
	RecordRecentImage(sess, []byte("SECOND"), "generated", ImageFromGenerated)

	// Next round: the model has read the result announcing SECOND.
	SnapshotImageRefs(sess)
	args := map[string]any{"path": "image#1"}
	if n := FreezeImageRefs(sess, args); n != 1 {
		t.Fatalf("froze %d refs, want 1", n)
	}
	got, _ := ResolveRecentImage(sess, args["path"].(string))
	if !bytes.Equal(got, []byte("SECOND")) {
		t.Errorf("image#1 still means the previous round's picture: %q", got)
	}
}

func TestOutOfRangeRefIsLeftForTheNormalError(t *testing.T) {
	attachmentTestDir(t)
	sess := freezeSession(t, "alice", "agent-wren")
	RecordRecentImage(sess, []byte("A-PHOTO"), "a photo", ImageFromUser)
	SnapshotImageRefs(sess)

	args := map[string]any{"path": "image#9"}
	if n := FreezeImageRefs(sess, args); n != 0 {
		t.Errorf("rewrote a position that does not exist")
	}
	if _, ok := ResolveRecentImage(sess, "image#9"); ok {
		t.Error("image#9 resolved to something")
	}
}
