package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCreateAutoScaffoldsUnsentWriteParam is the fix for the live moltbook
// failure: a POST comment action with content required but no body_template
// used to be REJECTED, which sent small local models into an unescapable retry
// loop (they never hand-wrote body_template, then hallucinated success). Now the
// framework AUTO-SCAFFOLDS a flat JSON body_template carrying the unsent params,
// so the write actually works and the loop never starts.
func TestCreateAutoScaffoldsUnsentWriteParam(t *testing.T) {
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
				"required": []any{"post_id", "content"}, // content lands nowhere → scaffold
			},
		},
	}
	res, err := createGrouped(create, sess)
	if err != nil {
		t.Fatalf("create should auto-scaffold, not reject: %v", err)
	}
	if !strings.Contains(res, "body_template") {
		t.Fatalf("success message should note the auto-added body_template; got: %s", res)
	}
	got, ok := loadExistingToolRecord(sess, "moltbook")
	if !ok {
		t.Fatalf("tool missing after create")
	}
	for _, a := range got.Actions {
		if a.Name != "comment" {
			continue
		}
		if !strings.Contains(a.BodyTemplate, "content") {
			t.Fatalf("auto-scaffold must carry the unsent 'content' param; body_template = %q", a.BodyTemplate)
		}
		// post_id already rides in the URL — it must NOT be duplicated into the body.
		if strings.Contains(a.BodyTemplate, "post_id") {
			t.Fatalf("URL param 'post_id' must not be scaffolded into the body; body_template = %q", a.BodyTemplate)
		}
		return
	}
	t.Fatalf("comment action missing after create")
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
