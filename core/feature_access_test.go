package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestFeatureAllowedForUser(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// No policy stored → open to everyone (non-breaking default on a live feature).
	if !FeatureAllowedForUser(db, "openai", "alice") {
		t.Error("absent policy must allow all users")
	}

	// Restrict to bob.
	SetFeatureAllowedUsers(db, "openai", []string{"bob"})
	if FeatureAllowedForUser(db, "openai", "alice") {
		t.Error("alice not in the allow-list — must be denied")
	}
	if !FeatureAllowedForUser(db, "openai", "bob") {
		t.Error("bob is listed — must be allowed")
	}
	if !FeatureAllowedForUser(db, "openai", "BOB") {
		t.Error("allow-list match should fold case")
	}

	// Clearing the list re-opens to everyone.
	SetFeatureAllowedUsers(db, "openai", nil)
	if !FeatureAllowedForUser(db, "openai", "alice") {
		t.Error("cleared allow-list must re-open to all")
	}

	// Empty feature/user never allowed.
	if FeatureAllowedForUser(db, "", "alice") || FeatureAllowedForUser(db, "openai", "") {
		t.Error("empty feature or user must deny")
	}
}

func TestShareableFeatureRegistry(t *testing.T) {
	RegisterShareableFeature(ShareableFeature{Key: "unit_feat_x", Label: "X"})
	RegisterShareableFeature(ShareableFeature{Key: "unit_feat_x", Label: "dupe"}) // idempotent
	if !IsShareableFeature("unit_feat_x") {
		t.Error("registered feature should report true")
	}
	if IsShareableFeature("never_registered") {
		t.Error("unregistered feature should report false")
	}
	n := 0
	for _, f := range ShareableFeatures() {
		if f.Key == "unit_feat_x" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("idempotent registration expected 1 entry, got %d", n)
	}
}
