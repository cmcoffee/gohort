package core

// Kept images: the durable half of the space. The property that matters is the
// one the ring can't offer — a name that still resolves after enough new
// pictures have arrived to have pushed the original out.

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestKeepPromotesOutOfTheRing(t *testing.T) {
	sess := imageSpaceSession(t)
	data := testPNG(t, 8, 8)
	RecordRecentImage(sess, data, "generated: a navy circle")

	kept, err := KeepImage(sess, "image#1", "Brand Mark", "the logo")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}
	if kept.Ref != "image#brand_mark" {
		t.Fatalf("ref = %q, want image#brand_mark (spaces folded, lowercased)", kept.Ref)
	}
	got, ok := ResolveRecentImage(sess, "image#brand_mark")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("a kept image must resolve through the same entry point as a positional ref")
	}
}

// The whole point: the ring prunes, the library doesn't.
func TestKeptImageSurvivesRingEviction(t *testing.T) {
	sess := imageSpaceSession(t)
	original := testPNG(t, 8, 8)
	RecordRecentImage(sess, original, "the one worth keeping")
	if _, err := KeepImage(sess, "image#1", "reference", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	// Push it well past the ring limit.
	for i := 0; i < recentImageLimit+3; i++ {
		RecordRecentImage(sess, testPNG(t, 4+i, 4+i), fmt.Sprintf("filler %d", i))
	}
	if _, ok := ResolveRecentImage(sess, "image#"+fmt.Sprint(recentImageLimit+1)); ok {
		t.Fatal("the ring should have pruned that far back")
	}
	got, ok := ResolveRecentImage(sess, "image#reference")
	if !ok || !bytes.Equal(got, original) {
		t.Fatal("a kept image must outlive the ring that produced it")
	}
}

func TestKeepRefusesNumericAndEmptyNames(t *testing.T) {
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "x")
	for _, name := range []string{"3", "", "   ", "___"} {
		if _, err := KeepImage(sess, "image#1", name, ""); err == nil {
			t.Fatalf("name %q was accepted; a bare number collides with positional refs and an empty name names nothing", name)
		}
	}
}

func TestKeepUnderSameNameReplaces(t *testing.T) {
	sess := imageSpaceSession(t)
	first, second := testPNG(t, 8, 8), testPNG(t, 16, 16)
	RecordRecentImage(sess, first, "v1")
	if _, err := KeepImage(sess, "image#1", "mark", ""); err != nil {
		t.Fatalf("keep v1: %v", err)
	}
	RecordRecentImage(sess, second, "v2")
	if _, err := KeepImage(sess, "image#1", "mark", ""); err != nil {
		t.Fatalf("keep v2: %v", err)
	}
	if n := len(KeptImages(sess)); n != 1 {
		t.Fatalf("kept count = %d, want 1 — a re-keep replaces rather than accumulating", n)
	}
	got, _ := ResolveRecentImage(sess, "image#mark")
	if !bytes.Equal(got, second) {
		t.Fatal("re-keeping a name must point it at the new image")
	}
}

func TestForgetRemovesAndReportsHonestly(t *testing.T) {
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "x")
	if _, err := KeepImage(sess, "image#1", "temp", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	gone, err := ForgetImage(sess, "temp")
	if err != nil || !gone {
		t.Fatalf("forget = (%v, %v), want (true, nil)", gone, err)
	}
	if _, ok := ResolveRecentImage(sess, "image#temp"); ok {
		t.Fatal("a forgotten name must stop resolving")
	}
	// Forgetting nothing must SAY it forgot nothing — the caller reports this
	// to the model, and "deleted" when nothing was deleted is a lie it acts on.
	if gone, _ := ForgetImage(sess, "never_existed"); gone {
		t.Fatal("forget reported a deletion that didn't happen")
	}
}

func TestKeptLibraryIsPerAgent(t *testing.T) {
	sess := imageSpaceSession(t)
	wren := &ToolSession{Username: sess.Username, AgentID: "wren", WorkspaceDir: sess.WorkspaceDir}
	other := &ToolSession{Username: sess.Username, AgentID: "other", WorkspaceDir: sess.WorkspaceDir}
	RecordRecentImage(wren, testPNG(t, 8, 8), "wren's")
	if _, err := KeepImage(wren, "image#1", "shared_name", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if _, ok := ResolveRecentImage(other, "image#shared_name"); ok {
		t.Fatal("one agent's kept reference must not resolve for another — same silent-wrong-answer failure the ring is scoped to avoid")
	}
}

func TestKeptManifestLeadsWithCaptionNotPixels(t *testing.T) {
	sess := imageSpaceSession(t)
	RecordRecentImage(sess, testPNG(t, 8, 8), "x")
	if _, err := KeepImage(sess, "image#1", "house_style", "match this"); err != nil {
		t.Fatalf("keep: %v", err)
	}
	m := KeptImageManifest(sess)
	if !strings.Contains(m, "image#house_style") || !strings.Contains(m, "match this") {
		t.Fatalf("manifest must name the ref and why it was kept; got %q", m)
	}
}

func TestCaptionImageIsBestEffort(t *testing.T) {
	// No session LLM wired: captioning must decline quietly rather than fail
	// the keep. A reference with no caption is still a usable reference.
	sess := imageSpaceSession(t)
	if c, d := CaptionImage(sess, testPNG(t, 8, 8)); c != "" || d != "" {
		t.Fatalf("caption without an LLM = (%q, %q), want both empty", c, d)
	}
	RecordRecentImage(sess, testPNG(t, 8, 8), "x")
	if _, err := KeepImage(sess, "image#1", "uncaptioned", ""); err != nil {
		t.Fatalf("keep must succeed without a caption: %v", err)
	}
}

// --- inheritance -------------------------------------------------------------

// keptImageFleet wires a parent chain for the duration of a test: child → parent.
func keptImageFleet(t *testing.T, links map[string]string) {
	t.Helper()
	saved := KeptImageParentAgent
	KeptImageParentAgent = func(_ *ToolSession, agentID string) string { return links[agentID] }
	t.Cleanup(func() { KeptImageParentAgent = saved })
}

func TestSubAgentInheritsParentKeptImage(t *testing.T) {
	sess := imageSpaceSession(t)
	keptImageFleet(t, map[string]string{"child": "parent"})
	parent := &ToolSession{Username: sess.Username, AgentID: "parent", WorkspaceDir: sess.WorkspaceDir}
	child := &ToolSession{Username: sess.Username, AgentID: "child", WorkspaceDir: sess.WorkspaceDir}

	logo := testPNG(t, 8, 8)
	RecordRecentImage(parent, logo, "the mark")
	if _, err := KeepImage(parent, "image#1", "brand_mark", "use everywhere"); err != nil {
		t.Fatalf("parent keep: %v", err)
	}
	got, ok := ResolveRecentImage(child, "image#brand_mark")
	if !ok || !bytes.Equal(got, logo) {
		t.Fatal("a sub-agent must resolve its parent's kept image — a fleet sharing one reference is the point")
	}
	var found *KeptImage
	for _, k := range KeptImages(child) {
		if k.Name == "brand_mark" {
			found = &k
		}
	}
	if found == nil || !found.Inherited || found.Owner != "parent" {
		t.Fatalf("listing must mark provenance; got %+v", found)
	}
}

func TestInheritanceIsOneDirectional(t *testing.T) {
	sess := imageSpaceSession(t)
	keptImageFleet(t, map[string]string{"child": "parent"})
	parent := &ToolSession{Username: sess.Username, AgentID: "parent", WorkspaceDir: sess.WorkspaceDir}
	child := &ToolSession{Username: sess.Username, AgentID: "child", WorkspaceDir: sess.WorkspaceDir}

	RecordRecentImage(child, testPNG(t, 8, 8), "child's own")
	if _, err := KeepImage(child, "image#1", "child_only", ""); err != nil {
		t.Fatalf("child keep: %v", err)
	}
	if _, ok := ResolveRecentImage(parent, "image#child_only"); ok {
		t.Fatal("a parent must NOT see what its sub-agent kept — inheritance flows down only")
	}
}

func TestOwnKeptImageShadowsInherited(t *testing.T) {
	sess := imageSpaceSession(t)
	keptImageFleet(t, map[string]string{"child": "parent"})
	parent := &ToolSession{Username: sess.Username, AgentID: "parent", WorkspaceDir: sess.WorkspaceDir}
	child := &ToolSession{Username: sess.Username, AgentID: "child", WorkspaceDir: sess.WorkspaceDir}

	parentImg, childImg := testPNG(t, 8, 8), testPNG(t, 16, 16)
	RecordRecentImage(parent, parentImg, "parent style")
	if _, err := KeepImage(parent, "image#1", "house_style", ""); err != nil {
		t.Fatalf("parent keep: %v", err)
	}
	RecordRecentImage(child, childImg, "child style")
	if _, err := KeepImage(child, "image#1", "house_style", ""); err != nil {
		t.Fatalf("child keep: %v", err)
	}
	got, _ := ResolveRecentImage(child, "image#house_style")
	if !bytes.Equal(got, childImg) {
		t.Fatal("the nearest library must win — an agent's own image shadows the inherited one")
	}
	// And the parent keeps its own.
	got, _ = ResolveRecentImage(parent, "image#house_style")
	if !bytes.Equal(got, parentImg) {
		t.Fatal("the child's keep must not have overwritten the parent's")
	}
	// Exactly one entry under that name for the child, not two.
	n := 0
	for _, k := range KeptImages(child) {
		if k.Name == "house_style" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("child sees %d entries named house_style, want 1", n)
	}
}

func TestForgetRefusesAnInheritedImage(t *testing.T) {
	sess := imageSpaceSession(t)
	keptImageFleet(t, map[string]string{"child": "parent"})
	parent := &ToolSession{Username: sess.Username, AgentID: "parent", WorkspaceDir: sess.WorkspaceDir}
	child := &ToolSession{Username: sess.Username, AgentID: "child", WorkspaceDir: sess.WorkspaceDir}

	RecordRecentImage(parent, testPNG(t, 8, 8), "the mark")
	if _, err := KeepImage(parent, "image#1", "brand_mark", ""); err != nil {
		t.Fatalf("parent keep: %v", err)
	}
	gone, err := ForgetImage(child, "brand_mark")
	if gone {
		t.Fatal("a sub-agent must not delete its parent's image")
	}
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("the refusal must name the owner rather than reporting a silent no-op; got %v", err)
	}
	// The parent's copy is untouched.
	if _, ok := ResolveRecentImage(parent, "image#brand_mark"); !ok {
		t.Fatal("the parent's image was removed by a sub-agent's forget")
	}
}

func TestInheritanceWalkSurvivesACycle(t *testing.T) {
	sess := imageSpaceSession(t)
	keptImageFleet(t, map[string]string{"a": "b", "b": "a"}) // mis-wired fleet
	a := &ToolSession{Username: sess.Username, AgentID: "a", WorkspaceDir: sess.WorkspaceDir}
	RecordRecentImage(a, testPNG(t, 8, 8), "x")
	if _, err := KeepImage(a, "image#1", "loop_safe", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	// Must terminate, not hang or stack-overflow.
	if _, ok := ResolveRecentImage(a, "image#loop_safe"); !ok {
		t.Fatal("resolution broke on a cyclic parent chain")
	}
}

// The two tiers answer different questions, so they're parsed apart from one
// reply: a label for listings, and the detail memory stores. A model that
// answers with one line only must still yield a usable label.
func TestCaptionSplitsLabelFromDetail(t *testing.T) {
	sess := imageSpaceSession(t)
	sess.LLM = &fakeCaptionLLM{reply: "Navy circular logo mark\n\nA dark navy circle centered on white, with a lowercase white sans-serif wordmark across the lower third. Flat, no gradients."}
	caption, description := CaptionImage(sess, testPNG(t, 8, 8))
	if caption != "Navy circular logo mark" {
		t.Errorf("caption = %q, want the first line only", caption)
	}
	if !strings.Contains(description, "lowercase white sans-serif") {
		t.Errorf("description lost the detail memory needs: %q", description)
	}
	sess.LLM = &fakeCaptionLLM{reply: "Just one line"}
	caption, description = CaptionImage(sess, testPNG(t, 8, 8))
	if caption != "Just one line" || description != "" {
		t.Errorf("one-line reply = (%q, %q), want the label and no detail", caption, description)
	}
}

func TestKeepStoresBothTiers(t *testing.T) {
	sess := imageSpaceSession(t)
	sess.LLM = &fakeCaptionLLM{reply: "A bar chart\n\nSix navy bars on a light grid, y-axis labelled in dollars, no legend."}
	RecordRecentImage(sess, testPNG(t, 8, 8), "x")
	if _, err := KeepImage(sess, "image#1", "chart_style", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	// Read back from disk — the sidecar has to carry both or the detail is
	// lost on the next process.
	all := KeptImages(sess)
	if len(all) != 1 || all[0].Caption != "A bar chart" || !strings.Contains(all[0].Description, "navy bars") {
		t.Fatalf("sidecar lost a tier: %+v", all)
	}
	// The manifest stays the SHORT one — it lists every kept image, and the
	// detailed text belongs in memory, not in a listing.
	if m := KeptImageManifest(sess); strings.Contains(m, "navy bars") {
		t.Errorf("manifest carried the detailed description:\n%s", m)
	}
}

// fakeCaptionLLM answers the vision pass with a canned reply.
type fakeCaptionLLM struct {
	LLM
	reply string
}

func (f *fakeCaptionLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	return &Response{Content: f.reply}, nil
}
