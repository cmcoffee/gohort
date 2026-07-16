package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestUpdateToolboxActionPreservesOthers verifies the update action edits one
// toolbox action in place while leaving the rest untouched — the fix for
// "recreate the whole toolbox to change one action." It also confirms the
// updated action's required split round-trips (an explicit [] stays optional).
func TestUpdateToolboxActionPreservesOthers(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":      "create",
		"name":        "svc",
		"description": "a service toolbox",
		"mode":        "toolbox",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":         "feed",
				"description":  "list",
				"url_template": "https://x.test/api/v1/posts?sort={sort}&limit={limit}",
				"params": map[string]any{
					"sort":  map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
				"required": []any{"sort", "limit"}, // start required
			},
			map[string]any{
				"name":         "profile",
				"description":  "me",
				"url_template": "https://x.test/api/v1/me",
				"params":       map[string]any{"_": map[string]any{"type": "string"}},
				"required":     []any{},
			},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update ONLY the feed action → make its params optional. profile must survive.
	upd := map[string]any{
		"name": "svc",
		"actions": []any{
			map[string]any{
				"name":         "feed",
				"description":  "list (optional params)",
				"url_template": "https://x.test/api/v1/posts?sort={sort}&limit={limit}",
				"params": map[string]any{
					"sort":  map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
				"required": []any{}, // now all optional
			},
		},
	}
	if _, err := updateGrouped(upd, sess); err != nil {
		t.Fatalf("update: %v", err)
	}

	rec, ok := loadExistingToolRecord(sess, "svc")
	if !ok {
		t.Fatal("tool vanished after update")
	}
	if len(rec.Actions) != 2 {
		t.Fatalf("expected 2 actions preserved, got %d (%v)", len(rec.Actions), actionNames(rec.Actions))
	}
	var feed, profile *TempToolAction
	for i := range rec.Actions {
		switch rec.Actions[i].Name {
		case "feed":
			feed = &rec.Actions[i]
		case "profile":
			profile = &rec.Actions[i]
		}
	}
	if feed == nil || profile == nil {
		t.Fatalf("both actions must survive; got %v", actionNames(rec.Actions))
	}
	if feed.Description != "list (optional params)" {
		t.Fatalf("feed not updated: %q", feed.Description)
	}
	if len(feed.Required) != 0 {
		t.Fatalf("feed.Required should be empty (all optional), got %v", feed.Required)
	}
	if profile.Description != "me" {
		t.Fatalf("profile was clobbered: %q", profile.Description)
	}

	// remove_actions drops profile, keeps feed.
	if _, err := updateGrouped(map[string]any{"name": "svc", "remove_actions": []any{"profile"}}, sess); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rec2, _ := loadExistingToolRecord(sess, "svc")
	if len(rec2.Actions) != 1 || rec2.Actions[0].Name != "feed" {
		t.Fatalf("remove_actions failed: %v", actionNames(rec2.Actions))
	}
}
