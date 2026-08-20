package core

import (
	"context"
	"testing"
)

// fakeRefSource is a plain ReferenceSource — no tool provider — so
// ReferenceItemTools* has to synthesize the default search tool for it.
type fakeRefSource struct {
	kind    string
	sawDone bool // whether the ctx handed to Fetch was already canceled
}

func (f *fakeRefSource) Kind() string  { return f.kind }
func (f *fakeRefSource) Label() string { return "Fakes" }
func (f *fakeRefSource) List(user string) []ReferenceItem {
	return []ReferenceItem{{ID: "item1", Name: "Widget Store"}}
}
func (f *fakeRefSource) Fetch(ctx context.Context, user, itemID, query string) string {
	f.sawDone = ctx.Err() != nil
	return "some text"
}

// The synthesized search tool must run on the TURN's context. Rooted on
// context.Background() a remote source keeps being waited on after the
// user stops the turn.
func TestSynthesizedSearchToolUsesTheSessionContext(t *testing.T) {
	src := &fakeRefSource{kind: "faketestsrc"}
	RegisterReferenceSource(src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defs := ReferenceItemToolsWithSession(&ToolSession{Ctx: ctx}, "u", "faketestsrc", "item1")
	if len(defs) != 1 {
		t.Fatalf("want the default search tool, got %d defs", len(defs))
	}
	if _, err := defs[0].Handler(map[string]any{"query": "anything"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !src.sawDone {
		t.Error("Fetch was handed a live context — a stopped turn cannot stop this source")
	}
}

// No session is legal: apps that never wired one keep the old behavior
// rather than panicking on a nil deref.
func TestNilSessionFallsBackToBackground(t *testing.T) {
	src := &fakeRefSource{kind: "faketestsrc2"}
	RegisterReferenceSource(src)

	defs := ReferenceItemTools("u", "faketestsrc2", "item1")
	if len(defs) != 1 {
		t.Fatalf("want the default search tool, got %d defs", len(defs))
	}
	if _, err := defs[0].Handler(map[string]any{"query": "anything"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if src.sawDone {
		t.Error("a nil session must give a live context, not a canceled one")
	}
}

// bothProviderSource implements BOTH shapes. The session-aware one has to
// win, or a source keeps its detached tools the moment it grows the plain
// method for a legacy caller.
type bothProviderSource struct{ fakeRefSource }

func (b *bothProviderSource) ItemTools(user, itemID string) []AgentToolDef {
	return []AgentToolDef{{Tool: Tool{Name: "plain_form"}}}
}
func (b *bothProviderSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	return []AgentToolDef{{Tool: Tool{Name: "session_form"}}}
}

func TestSessionAwareProviderWinsOverThePlainOne(t *testing.T) {
	RegisterReferenceSource(&bothProviderSource{fakeRefSource{kind: "faketestsrc3"}})

	defs := ReferenceItemToolsWithSession(&ToolSession{}, "u", "faketestsrc3", "item1")
	if len(defs) != 1 || defs[0].Tool.Name != "session_form" {
		t.Fatalf("want the session-aware provider to win, got %+v", defs)
	}
	// And the legacy entry point still resolves through it, with a nil
	// session — one code path, not two.
	if defs := ReferenceItemTools("u", "faketestsrc3", "item1"); len(defs) != 1 || defs[0].Tool.Name != "session_form" {
		t.Fatalf("legacy entry point should route through the same provider, got %+v", defs)
	}
}
