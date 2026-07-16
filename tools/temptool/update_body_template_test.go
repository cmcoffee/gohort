package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCreateRejectsUnsentWriteParam is the primary defense for the live
// moltbook failure: a POST comment action with content required but no
// body_template sends content nowhere. Authoring it must be REJECTED with a
// specific error — not silently created, then 400 at run time (which fed a
// retry loop the model never escaped).
func TestCreateRejectsUnsentWriteParam(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":      "create",
		"name":        "moltbook",
		"description": "social toolbox",
		"mode":        "toolbox",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":         "comment",
				"description":  "comment on a post",
				"url_template": "https://x.test/api/v1/posts/{post_id}/comments",
				"method":       "POST",
				"params": map[string]any{
					"post_id": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "content"}, // content sent nowhere
			},
		},
	}
	_, err := createGrouped(create, sess)
	if err == nil {
		t.Fatalf("expected create to be REJECTED for the unsent 'content' param")
	}
	if !strings.Contains(err.Error(), "content") || !strings.Contains(err.Error(), "body_template") {
		t.Fatalf("rejection must name the unsent param and body_template; got: %v", err)
	}
}

// TestCreateAllowsWiredWriteAndUpdatePersists confirms the gate passes a
// correctly wired write action, and that a later update to a different
// body_template persists (the framework path the model FALSELY believed was
// broken — it was actually looping without ever sending body_template).
func TestCreateAllowsWiredWriteAndUpdatePersists(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":      "create",
		"name":        "moltbook",
		"description": "social toolbox",
		"mode":        "toolbox",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":          "comment",
				"description":   "comment on a post",
				"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
				"method":        "POST",
				"body_template": `{"content": {content}}`,
				"params": map[string]any{
					"post_id": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "content"},
			},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("wired create should pass the gate: %v", err)
	}

	upd := map[string]any{
		"name": "moltbook",
		"actions": []any{
			map[string]any{
				"name":          "comment",
				"description":   "comment on a post",
				"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
				"method":        "POST",
				"body_template": `{"body": {content}}`, // different wrapping key
				"params": map[string]any{
					"post_id": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "content"},
			},
		},
	}
	if _, err := updateGrouped(upd, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok := loadExistingToolRecord(sess, "moltbook")
	if !ok {
		t.Fatalf("tool vanished after update")
	}
	for i := range got.Actions {
		if got.Actions[i].Name == "comment" {
			if !strings.Contains(got.Actions[i].BodyTemplate, `"body"`) {
				t.Fatalf("update did not persist the new body_template — got %q", got.Actions[i].BodyTemplate)
			}
			return
		}
	}
	t.Fatalf("comment action missing after update")
}
