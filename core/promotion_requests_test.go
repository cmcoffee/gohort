package core

import (
	"testing"

	"github.com/cmcoffee/gohort/core/promotion"
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
	id := PromotionRequestKey("tool", "alice", "weather")
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

// TestApprovePromotionRunsTheKindsApprover covers the approve seam: a kind
// with no approver is refused and stays pending; an approver that fails
// leaves the row pending; a successful one marks the row approved with the
// decider, and the approver saw the request's owner and name.
func TestApprovePromotionRunsTheKindsApprover(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if err := CreatePromotionRequest(db, "alice", "widget", "spinner", ""); err != nil {
		t.Fatal(err)
	}
	id := PromotionRequestKey("widget", "alice", "spinner")

	if err := promotion.Approve(db, id, "root"); err == nil {
		t.Fatal("a kind with no approver must be refused")
	}
	if !PendingPromotion(db, "alice", "widget", "spinner") {
		t.Fatal("a refused approval must leave the request pending")
	}

	fail := true
	var got []string
	promotion.RegisterApprover("widget", func(owner, name string) error {
		got = append(got, owner+"/"+name)
		if fail {
			return Error("no")
		}
		return nil
	})
	if err := promotion.Approve(db, id, "root"); err == nil {
		t.Fatal("a failing approver must surface its error")
	}
	if !PendingPromotion(db, "alice", "widget", "spinner") {
		t.Fatal("a failed side effect must leave the request pending")
	}

	fail = false
	if err := promotion.Approve(db, id, "root"); err != nil {
		t.Fatal(err)
	}
	req, _ := GetPromotionRequest(db, id)
	if req.State != PromotionApprovedState || req.DecidedBy != "root" {
		t.Fatalf("approved row = %+v", req)
	}
	if len(got) != 2 || got[1] != "alice/spinner" {
		t.Fatalf("approver calls = %v", got)
	}
	if err := promotion.Approve(db, "widget:nobody:x", "root"); err == nil {
		t.Fatal("an unknown request id must error")
	}
}
