package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Builder death-loop regression (Moltbook, 2026-07-24): actionToArgs /
// tempToolToCreateArgs emit required as a native []string, but stringSliceArg
// only accepted []any — so EVERY action not explicitly re-sent by the caller
// lost its required list on the update round-trip. For an action with a PATH
// placeholder ({post_id} in the URL), the path-placeholder gate then rejected
// the whole update — remove_actions and single-action upserts were unfixable
// from the model's side, and a "successful" full re-send didn't help the next
// partial call. These tests pin the round-trip with path-param actions, which
// the older TestUpdateToolboxActionPreservesOthers (query-string params only)
// couldn't catch.

func pathToolboxCreateArgs() map[string]any {
	return map[string]any{
		"action":      "create",
		"name":        "molt",
		"description": "social toolbox",
		"mode":        "toolbox",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":          "reply_to_post",
				"description":   "reply to a post",
				"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
				"method":        "POST",
				"body_template": `{"content": {content}}`,
				"params": map[string]any{
					"post_id": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "content"},
			},
			map[string]any{
				"name":          "reply_to_comment",
				"description":   "reply to a comment",
				"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
				"method":        "POST",
				"body_template": `{"parent_id": {comment_id}, "content": {content}}`,
				"params": map[string]any{
					"post_id":    map[string]any{"type": "string"},
					"comment_id": map[string]any{"type": "string"},
					"content":    map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "comment_id", "content"},
			},
		},
	}
}

// remove_actions alone must succeed on a toolbox whose surviving actions carry
// path-placeholder params — the survivors' required lists ride the round-trip.
func TestRemoveActionsPreservesPathRequired(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	if _, err := createGrouped(pathToolboxCreateArgs(), sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := updateGrouped(map[string]any{
		"name": "molt", "remove_actions": []any{"reply_to_comment"},
	}, sess); err != nil {
		t.Fatalf("remove_actions must not trip the path-placeholder gate on round-tripped actions: %v", err)
	}
	rec, ok := loadExistingToolRecord(sess, "molt")
	if !ok {
		t.Fatal("tool vanished after update")
	}
	if len(rec.Actions) != 1 || rec.Actions[0].Name != "reply_to_post" {
		t.Fatalf("expected only reply_to_post to survive, got %v", actionNames(rec.Actions))
	}
	got := strings.Join(rec.Actions[0].Required, ",")
	if got != "post_id,content" {
		t.Fatalf("survivor's required list mangled by the round-trip: %v", rec.Actions[0].Required)
	}
}

// Upserting ONE action must leave the other action's required list intact —
// the partial-edit shape Builder looped on (each fix erroring about the OTHER,
// round-tripped action).
func TestSingleActionUpsertPreservesOtherActionRequired(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s2",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	if _, err := createGrouped(pathToolboxCreateArgs(), sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := updateGrouped(map[string]any{
		"name": "molt",
		"actions": []any{map[string]any{
			"name":        "reply_to_post",
			"description": "reply to a post (edited)",
		}},
	}, sess); err != nil {
		t.Fatalf("single-action upsert must not fail on the untouched action: %v", err)
	}
	rec, _ := loadExistingToolRecord(sess, "molt")
	for _, a := range rec.Actions {
		if a.Name == "reply_to_comment" {
			if strings.Join(a.Required, ",") != "post_id,comment_id,content" {
				t.Fatalf("untouched action's required list mangled: %v", a.Required)
			}
			return
		}
	}
	t.Fatalf("reply_to_comment missing after upsert: %v", actionNames(rec.Actions))
}

// A standalone api tool's top-level required list rides the same round-trip
// (tempToolToCreateArgs emits []string): an unrelated partial edit must not
// wipe it.
func TestApiToolRequiredSurvivesUnrelatedUpdate(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prevAuth }()
	if err := Secure().Save(SecureCredential{Name: "no_auth", Type: SecureCredNone, BaseURL: "https://x.test"}, ""); err != nil {
		t.Fatalf("register credential: %v", err)
	}
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s3",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	if _, err := createGrouped(map[string]any{
		"action":        "create",
		"name":          "reply_one",
		"description":   "reply to a comment",
		"mode":          "api",
		"credential":    "no_auth",
		"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
		"method":        "POST",
		"body_template": `{"content": {content}}`,
		"params": map[string]any{
			"post_id": map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []any{"post_id", "content"},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := updateGrouped(map[string]any{
		"name": "reply_one", "description": "reply to a comment (edited)",
	}, sess); err != nil {
		t.Fatalf("description-only update: %v", err)
	}
	rec, ok := loadExistingToolRecord(sess, "reply_one")
	if !ok {
		t.Fatal("tool vanished after update")
	}
	if strings.Join(rec.Required, ",") != "post_id,content" {
		t.Fatalf("top-level required mangled on round-trip: %v", rec.Required)
	}
}

// Delete symmetry: a tool bundled to ANOTHER of the user's agents resolves via
// FindUserAgentTool and is removed from THAT agent's record via
// DetachToolFromAgent — mirroring update's in-place redirect. Before this,
// delete reported "no temp tool named X" (or silently removed only a session
// shadow while the record copy reloaded next turn).
func TestDeleteAgentBundledToolRemovesInPlace(t *testing.T) {
	prevFind, prevDetach := FindUserAgentTool, DetachToolFromAgent
	defer func() { FindUserAgentTool, DetachToolFromAgent = prevFind, prevDetach }()

	store := map[string]TempTool{"molt_reply": {
		Name:        "molt_reply",
		Description: "standalone reply tool",
		Mode:        TempToolModeAPI,
		Credential:  "no_auth",
	}}
	FindUserAgentTool = func(db Database, owner, name string) (TempTool, string, bool) {
		tt, ok := store[name]
		return tt, "moltbook-agent", ok
	}
	var detachedFrom, detachedName string
	DetachToolFromAgent = func(db Database, owner, agentID, toolName string) error {
		detachedFrom, detachedName = agentID, toolName
		delete(store, toolName)
		return nil
	}

	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "builder-sess",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	res, err := deleteGrouped(map[string]any{"name": "molt_reply"}, sess)
	if err != nil {
		t.Fatalf("cross-agent delete: %v", err)
	}
	if detachedFrom != "moltbook-agent" || detachedName != "molt_reply" {
		t.Fatalf("expected detach from the owning agent, got agent=%q tool=%q", detachedFrom, detachedName)
	}
	if !strings.Contains(res, "moltbook-agent") {
		t.Errorf("result should name the owning agent: %q", res)
	}
	if _, still := store["molt_reply"]; still {
		t.Error("tool must be gone from the agent record")
	}
}
