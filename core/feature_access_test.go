package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cmcoffee/oddjob/core/appagents"
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

// Per-app key gate: an app-owned agent dispatched through a key-authenticated
// surface requires the app's feature on the key; ordinary agents never gate;
// nil-scope legacy keys grandfather.
func TestKeyAllowsAppAgent(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "test-app-agent-x", OwningApp: "Testify", Name: "T", Hidden: true,
	})
	db := &DBase{Store: kvlite.MemStore()}

	// Ordinary agent: no gate.
	if ok, _ := KeyAllowsAppAgent(db, "alice", &AccountToken{Scope: &TokenScope{}}, "some-user-agent"); !ok {
		t.Fatal("non-app agents must never be gated")
	}
	// Scoped key WITHOUT the app feature: denied, message names the app.
	tok := &AccountToken{Scope: &TokenScope{Features: []string{"openai"}}}
	ok, msg := KeyAllowsAppAgent(db, "alice", tok, "test-app-agent-x")
	if ok {
		t.Fatal("scoped key without app:testify must be denied")
	}
	if !strings.Contains(msg, "Testify") {
		t.Errorf("denial should name the app: %q", msg)
	}
	// Scoped key WITH it: allowed.
	tok2 := &AccountToken{Scope: &TokenScope{Features: []string{"app:testify"}}}
	if ok, _ := KeyAllowsAppAgent(db, "alice", tok2, "test-app-agent-x"); !ok {
		t.Fatal("key with app:testify must pass")
	}
	// Legacy nil-scope key: grandfathered.
	if ok, _ := KeyAllowsAppAgent(db, "alice", &AccountToken{}, "test-app-agent-x"); !ok {
		t.Fatal("nil-scope legacy key must grandfather")
	}
	// Admin deny wins over everything.
	SetFeatureAllowedUsers(db, "app:testify", []string{"bob"})
	if ok, _ := KeyAllowsAppAgent(db, "alice", tok2, "test-app-agent-x"); ok {
		t.Fatal("admin allow-list without alice must deny")
	}
}

// The admin keeps feature policies in the root store. An app that reads one
// through its own bucket (T.DB is a bucket of the root store) finds nothing,
// and nothing reads as "everyone": that is how /v1 and MCP ignored every
// admin restriction. Every policy read in an app names the root store.
func TestAppsReadFeaturePoliciesFromTheRootStore(t *testing.T) {
	call := regexp.MustCompile(`(FeatureAllowedForUser|KeyAllowsAppAgent|LoadFeaturePolicy|SetFeatureAllowedUsers)\(\s*([^,]+),`)
	ok := map[string]bool{"RootDB": true, "AuthDB()": true, "a.db": true}
	filepath.WalkDir("../apps", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, _ := os.ReadFile(path)
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			if !ok[strings.TrimSpace(m[2])] {
				t.Errorf("%s: %s(%s, ...) reads a feature policy outside the root store", path, m[1], m[2])
			}
		}
		return nil
	})
}
