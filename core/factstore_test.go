package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fakeChat builds a FactChatFunc that returns a canned reply (or error),
// so the deterministic half of supersession can be tested without a live
// model. The embedding band + real judge are exercised by the manual smoke
// test, not here.
func fakeChat(reply string, err error) FactChatFunc {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		if err != nil {
			return nil, err
		}
		return &Response{Content: reply}, nil
	}
}

// TestJudgeSupersedes pins the parse-and-select logic and every fail-safe
// path: a bad reply must yield NO supersession (the new fact is just added
// alongside), never a wrong one.
func TestJudgeSupersedes(t *testing.T) {
	cands := []MemoryFact{
		{ID: "a", Namespace: "ns", Note: "lives in Denver"},
		{ID: "b", Namespace: "ns", Note: "likes coffee"},
	}
	cases := []struct {
		name string
		chat FactChatFunc
		want []string // expected superseded IDs
	}{
		{"replace first", fakeChat("[1]", nil), []string{"a"}},
		{"replace both", fakeChat("[1,2]", nil), []string{"a", "b"}},
		{"replace none", fakeChat("[]", nil), nil},
		{"index out of range ignored", fakeChat("[5]", nil), nil},
		{"mixed valid + out of range", fakeChat("[2,9]", nil), []string{"b"}},
		{"malformed reply -> none", fakeChat("none of them apply", nil), nil},
		{"chat error -> none", fakeChat("", errors.New("boom")), nil},
		{"nil chat -> none", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := judgeSupersedes(tc.chat, "moved to Austin", cands)
			var gotIDs []string
			for _, f := range got {
				gotIDs = append(gotIDs, f.ID)
			}
			if !reflect.DeepEqual(gotIDs, tc.want) {
				t.Fatalf("got %v, want %v", gotIDs, tc.want)
			}
		})
	}
}

// TestListMemoryFactsFiltersSuperseded: a row marked SupersededAt is hidden
// from the central list (which feeds render, recall, dedup, and forget).
func TestListMemoryFactsFiltersSuperseded(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(MemoryFactsTable, factDBKey(ns, "a"), MemoryFact{
		Namespace: ns, ID: "a", Note: "active fact", Created: time.Now(),
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "b"), MemoryFact{
		Namespace: ns, ID: "b", Note: "old fact",
		Created: time.Now().Add(-time.Hour), SupersededAt: time.Now(), SupersededBy: "a",
	})
	got := ListMemoryFacts(db, ns)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("expected only the active fact, got %+v", got)
	}
}

// TestForgetByIndexSkipsSuperseded confirms the index the LLM sees (built
// from the active-only list) lines up with what forget removes — a
// superseded row must not shift the indices.
func TestForgetByIndexSkipsSuperseded(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(MemoryFactsTable, factDBKey(ns, "old"), MemoryFact{
		Namespace: ns, ID: "old", Note: "stale", Created: time.Now().Add(-2 * time.Hour), SupersededAt: time.Now(),
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "keep"), MemoryFact{
		Namespace: ns, ID: "keep", Note: "current", Created: time.Now(),
	})
	// Active list is just ["current"] at index 1. Forgetting 1 must remove
	// "keep", not the hidden superseded "old".
	removed, ok := ForgetMemoryFactByIndex(db, ns, 1)
	if !ok || removed.ID != "keep" {
		t.Fatalf("expected to remove 'keep', got ok=%v id=%q", ok, removed.ID)
	}
}

// TestStoreMemoryFactNoChatStillDedups: the variadic chat arg is optional —
// callers that pass none keep the prior dedup-only behavior. Tier-1
// normalized match needs no embeddings, so this runs offline.
func TestStoreMemoryFactNoChatStillDedups(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	if _, isNew, _ := StoreMemoryFact(db, ns, "User prefers metric units"); !isNew {
		t.Fatal("first store should report new")
	}
	if _, isNew, _ := StoreMemoryFact(db, ns, "  user prefers metric units. "); isNew {
		t.Fatal("normalized duplicate should not report new")
	}
	if got := ListMemoryFacts(db, ns); len(got) != 1 {
		t.Fatalf("expected 1 fact after dedup, got %d", len(got))
	}
}
