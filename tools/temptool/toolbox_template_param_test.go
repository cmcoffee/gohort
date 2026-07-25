package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Toolbox actions must get the same template↔params gate the standalone api
// create has: every identifier-shaped {placeholder} in url_template /
// body_template names a declared param. The Moltbook regression (2026-07-24):
// an update dropped the comment_id PARAM from reply_to_comment while its
// body_template still said {"parent_id": {comment_id}, ...} — authoring
// accepted it, and every dispatch died with "body template substitution
// produced invalid JSON". Worse, the "fix" that finally passed removed the
// parent threading entirely. Rejecting at authoring points the author at the
// real repair: drop the param and fix the template in the same call.
func TestToolboxRejectsTemplateReferencingDroppedParam(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s-tpl",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "molttpl", "description": "d",
		"mode": "toolbox", "credential": "no_auth",
		"actions": []any{map[string]any{
			"name":          "reply_to_comment",
			"description":   "threaded reply",
			"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
			"method":        "POST",
			"body_template": `{"parent_id": {comment_id}, "content": {content}}`,
			"params": map[string]any{
				"post_id":    map[string]any{"type": "string"},
				"comment_id": map[string]any{"type": "string"},
				"content":    map[string]any{"type": "string"},
			},
			"required": []any{"post_id", "comment_id", "content"},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Drop comment_id from params WITHOUT touching body_template → reject.
	_, err := updateGrouped(map[string]any{
		"name": "molttpl",
		"actions": []any{map[string]any{
			"name": "reply_to_comment",
			"params": map[string]any{
				"post_id": map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []any{"post_id", "content"},
		}},
	}, sess)
	if err == nil {
		t.Fatal("dropping a param the body_template references must be rejected at authoring, not die at dispatch")
	}
	if !strings.Contains(err.Error(), "comment_id") || !strings.Contains(err.Error(), "body_template") {
		t.Fatalf("error should name the stranded placeholder and the template: %v", err)
	}

	// Same drop WITH the template fixed in the same call → accepted.
	if _, err := updateGrouped(map[string]any{
		"name": "molttpl",
		"actions": []any{map[string]any{
			"name":          "reply_to_comment",
			"body_template": `{"content": {content}}`,
			"params": map[string]any{
				"post_id": map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []any{"post_id", "content"},
		}},
	}, sess); err != nil {
		t.Fatalf("dropping the param and fixing the template together must pass: %v", err)
	}

	// url_template placeholders are gated the same way.
	_, err = updateGrouped(map[string]any{
		"name": "molttpl",
		"actions": []any{map[string]any{
			"name":         "reply_to_comment",
			"url_template": "https://x.test/api/v1/posts/{post_id}/comments/{thread_id}",
		}},
	}, sess)
	if err == nil || !strings.Contains(err.Error(), "thread_id") {
		t.Fatalf("url_template referencing an undeclared param must be rejected, got: %v", err)
	}
}
