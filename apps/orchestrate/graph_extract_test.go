package orchestrate

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// fakeChat is a FactChatFunc that returns canned content, standing in for the
// worker LLM so extraction can be tested without a model.
func fakeChat(content string) FactChatFunc {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: content}, nil
	}
}

// TestJudgeGraphTriples: canned JSON parses into triples; malformed or empty
// inputs fail safe to nil so nothing gets written.
func TestJudgeGraphTriples(t *testing.T) {
	got := judgeGraphTriples(fakeChat(`[{"subject":"A","subject_kind":"person","relation":"knows","object":"B","object_kind":"person"}]`), "A knows B")
	if len(got) != 1 || got[0].Subject != "A" || got[0].Relation != "knows" || got[0].Object != "B" {
		t.Fatalf("parse failed: %+v", got)
	}
	if got := judgeGraphTriples(fakeChat("sorry, no JSON here"), "x"); got != nil {
		t.Fatalf("malformed reply must yield nil, got %+v", got)
	}
	if got := judgeGraphTriples(fakeChat("[]"), "   "); got != nil {
		t.Fatalf("empty text must yield nil (no chat)")
	}
}

// TestExtractGraphFromText: valid triples become graph edges stamped observed;
// entities alias-merge by name; empty/self-loop triples are skipped.
func TestExtractGraphFromText(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := "test-ns"
	chat := fakeChat(`[
		{"subject":"Craig","subject_kind":"person","relation":"owns","object":"Hansel","object_kind":"thing"},
		{"subject":"Craig","subject_kind":"person","relation":"lives in","object":"Santa Cruz","object_kind":"place"},
		{"subject":"","relation":"x","object":"y"},
		{"subject":"Loop","relation":"is","object":"Loop"}
	]`)

	n := extractGraphFromText(db, ns, "Craig owns Hansel and lives in Santa Cruz.", chat)
	if n != 2 {
		t.Fatalf("expected 2 edges (empty + self-loop skipped), got %d", n)
	}

	craig, ok := FindGraphEntity(db, ns, "Craig")
	if !ok {
		t.Fatal("Craig entity not created")
	}
	edges := GraphEdgesFrom(db, ns, craig.ID)
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges from Craig (alias-merged to one node), got %d", len(edges))
	}
	for _, e := range edges {
		if e.Source != MemSourceObserved {
			t.Errorf("extracted edge %q should be Source=observed, got %v", e.Rel, e.Source)
		}
	}
}

// TestFoldUserText: the fold extraction input is the USER turns of the batch
// both sides, each labelled, whitespace-only messages skipped, and capped.
func TestFoldExtractTextShape(t *testing.T) {
	folded := []Message{
		{Role: "user", Content: "Craig owns a dog named Hansel."},
		{Role: "assistant", Content: "Noted — Hansel the dog."},
		{Role: "user", Content: "  Craig works at Acme.  "},
		{Role: "assistant", Content: "Got it."},
		{Role: "user", Content: "   "}, // whitespace-only → skipped
	}
	got := foldExtractText(folded)
	want := "USER SAID:\nCraig owns a dog named Hansel.\nCraig works at Acme.\n\nASSISTANT SAID:\nNoted — Hansel the dog.\nGot it."
	if got != want {
		t.Fatalf("foldExtractText =\n%q\nwant\n%q", got, want)
	}
	if foldExtractText(nil) != "" {
		t.Fatal("empty batch should yield empty string")
	}

	// Cap: a very long user turn is truncated to the max, and leaves no room
	// for the assistant half.
	big := strings.Repeat("x", foldExtractMaxChars+500)
	got = foldExtractText([]Message{{Role: "user", Content: big}})
	if !strings.HasPrefix(got, "USER SAID:\n") {
		t.Fatalf("expected the user label, got %.20q", got)
	}
	// The budget covers the ASSEMBLED string, labels included — it exists so
	// the worker prompt cannot blow up, and a cap measuring only the content
	// is one the labels walk straight past.
	if len(got) > foldExtractMaxChars {
		t.Fatalf("assembled input outgrew its budget: %d > %d", len(got), foldExtractMaxChars)
	}
	if len(got) < foldExtractMaxChars-64 {
		t.Fatalf("the user half should fill the budget, got %d", len(got))
	}
}

// TestMaybeExtractGraphShortCircuits: the synchronous guards (gate off, text too
// short) write nothing and never spawn the background pass.
func TestMaybeExtractGraphShortCircuits(t *testing.T) {
	longText := "Craig owns a dog named Hansel and works at Acme in Santa Cruz."
	triples := `[{"subject":"Craig","relation":"owns","object":"Hansel"}]`

	// Gate off → nothing, even with valid long text.
	off := &DBase{Store: kvlite.MemStore()}
	off.Set(WebTable, TunableGraphExtract, float64(0))
	SetTunablesDB(off)
	db1 := &DBase{Store: kvlite.MemStore()}
	maybeExtractGraph(db1, "ns", longText, fakeChat(triples))
	if ents := ListGraphEntities(db1, "ns"); len(ents) != 0 {
		SetTunablesDB(nil)
		t.Fatalf("gate off must write nothing, got %d entities", len(ents))
	}

	// Gate on but text below the length floor → nothing (no goroutine).
	on := &DBase{Store: kvlite.MemStore()}
	on.Set(WebTable, TunableGraphExtract, float64(1))
	SetTunablesDB(on)
	db2 := &DBase{Store: kvlite.MemStore()}
	maybeExtractGraph(db2, "ns", "hi", fakeChat(triples))
	if ents := ListGraphEntities(db2, "ns"); len(ents) != 0 {
		SetTunablesDB(nil)
		t.Fatalf("too-short text must write nothing, got %d entities", len(ents))
	}
	SetTunablesDB(nil)
}

// TestFoldUserTextRuneSafe: the fold-extraction cap must cut on a rune
// boundary — a byte-index slice could split a UTF-8 sequence and hand the
// worker prompt an invalid trailing byte.
func TestFoldUserTextRuneSafe(t *testing.T) {
	long := "a" + strings.Repeat("€", 4000) // 3-byte rune, misaligned by the leading ascii byte
	out := foldExtractText([]Message{{Role: "user", Content: long}})
	if len(out) > foldExtractMaxChars {
		t.Fatalf("cap not applied: %d bytes", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("cap split a UTF-8 sequence")
	}
}
