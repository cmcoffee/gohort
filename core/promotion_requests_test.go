package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestPromotionRequests covers the publish-request queue: required fields, the
// pending indicator, idempotent re-request per (kind,owner,name), the
// pending-only vs full listing, decision recording, and re-open after a decision.
func TestPromotionRequests(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	if err := CreatePromotionRequest(db, "", "tool", "x", ""); err == nil {
		t.Fatal("a missing owner must error")
	}

	if err := CreatePromotionRequest(db, "alice", "tool", "weather", "please"); err != nil {
		t.Fatal(err)
	}
	if !PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("weather must be pending after a request")
	}
	if PendingPromotion(db, "bob", "tool", "weather") {
		t.Fatal("bob filed no request")
	}

	// Idempotent per triple: a re-request updates the note in place, not a dup.
	if err := CreatePromotionRequest(db, "alice", "tool", "weather", "updated"); err != nil {
		t.Fatal(err)
	}
	pend := ListPromotionRequests(db, true)
	if len(pend) != 1 || pend[0].Note != "updated" {
		t.Fatalf("re-request must update in place; got %+v", pend)
	}

	// A distinct tool adds a row.
	if err := CreatePromotionRequest(db, "alice", "tool", "jira", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(ListPromotionRequests(db, true)); n != 2 {
		t.Fatalf("expected 2 pending; got %d", n)
	}

	// Approve clears pending; the pending-only list drops it, the full list keeps it.
	id := promotionRequestKey("tool", "alice", "weather")
	if err := SetPromotionRequestState(db, id, PromotionApprovedState, "admin"); err != nil {
		t.Fatal(err)
	}
	if PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("an approved request must not read as pending")
	}
	if n := len(ListPromotionRequests(db, true)); n != 1 {
		t.Fatalf("only jira should remain pending; got %d", n)
	}
	if n := len(ListPromotionRequests(db, false)); n != 2 {
		t.Fatalf("the full list keeps the approved row; got %d", n)
	}
	if req, _ := GetPromotionRequest(db, id); req.State != PromotionApprovedState || req.DecidedBy != "admin" {
		t.Fatalf("decision not recorded; got %+v", req)
	}

	// Re-request after a decision re-opens it as pending.
	if err := CreatePromotionRequest(db, "alice", "tool", "weather", "again"); err != nil {
		t.Fatal(err)
	}
	if !PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("a re-request after approval must re-open as pending")
	}
}
