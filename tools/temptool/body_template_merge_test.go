package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func mbToolboxCreateArgs() map[string]any {
	return map[string]any{
		"name":        "mb",
		"description": "moltbook",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":         "reply",
				"description":  "reply to a comment",
				"url_template": "https://x.test/reply",
				"method":       "POST",
				"params": map[string]any{
					"comment_id": map[string]any{"type": "string"},
					"content":    map[string]any{"type": "string"},
				},
				"required": []any{"comment_id", "content"},
				// Explicit body maps the comment_id VALUE onto the API's parent_id field.
				"body_template": `{"parent_id": {comment_id}, "content": {content}}`,
			},
		},
	}
}

func lookupAction(tt *TempTool, name string) *TempToolAction {
	if tt == nil {
		return nil
	}
	for i := range tt.Actions {
		if tt.Actions[i].Name == name {
			return &tt.Actions[i]
		}
	}
	return nil
}

// TestToolboxUpdatePreservesBodyTemplate pins the field-merge fix: updating one
// action's params WITHOUT re-supplying body_template must keep the explicit
// body — not drop it and let the write-action scaffold regenerate a param-name
// guess (the "I set parent_id but it reverted to comment_id" loop).
func TestToolboxUpdatePreservesBodyTemplate(t *testing.T) {
	sess := newTestSession()
	if _, err := createToolboxGrouped(mbToolboxCreateArgs(), sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if act := lookupAction(sess.LookupTempTool("mb"), "reply"); act == nil || !strings.Contains(act.BodyTemplate, "parent_id") {
		t.Fatalf("setup: explicit body not stored: %+v", act)
	}

	// Update touching ONLY params/required (add optional post_id). No body_template.
	upd := map[string]any{
		"name": "mb",
		"actions": []any{
			map[string]any{
				"name": "reply",
				"params": map[string]any{
					"comment_id": map[string]any{"type": "string"},
					"content":    map[string]any{"type": "string"},
					"post_id":    map[string]any{"type": "string"},
				},
				"required": []any{"comment_id", "content"},
			},
		},
	}
	if _, err := updateGrouped(upd, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	act := lookupAction(sess.LookupTempTool("mb"), "reply")
	if act == nil {
		t.Fatal("action lost after update")
	}
	if !strings.Contains(act.BodyTemplate, "parent_id") {
		t.Errorf("body_template dropped on merge update — got %q, want it to still map parent_id", act.BodyTemplate)
	}
	if _, ok := act.Params["post_id"]; !ok {
		t.Errorf("merge lost the newly added post_id param")
	}
}

// TestToolboxUpdateRejectsTopLevelPerActionField pins the redirect guard: a
// per-action field passed at the TOP level of a toolbox update is rejected
// (was silently ignored), pointing the caller at actions=[{...}].
func TestToolboxUpdateRejectsTopLevelPerActionField(t *testing.T) {
	sess := newTestSession()
	if _, err := createToolboxGrouped(mbToolboxCreateArgs(), sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := updateGrouped(map[string]any{
		"name":          "mb",
		"body_template": `{"parent_id": {comment_id}, "content": {content}}`,
	}, sess)
	if err == nil || !strings.Contains(err.Error(), "PER-ACTION") {
		t.Errorf("expected a per-action redirect error, got: %v", err)
	}
}
