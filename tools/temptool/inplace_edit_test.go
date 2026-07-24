package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Builder editing a tool that lives on ANOTHER of the user's agents must resolve
// it there and write the edit back IN PLACE — onto that agent's record — not
// promote it into the user-wide pool. This is the "let Builder repair any of the
// user's tools" path: FindUserAgentTool resolves it, BundleAuthoredToolTo
// redirects the write-back.
func TestUpdateAgentBundledToolWritesBackInPlace(t *testing.T) {
	prevFind, prevAttach := FindUserAgentTool, AttachToolToAgent
	defer func() { FindUserAgentTool, AttachToolToAgent = prevFind, prevAttach }()

	// A tool bundled to agent "moltbook-agent", invisible to Builder's own pools.
	bundled := TempTool{
		Name:        "moltbook",
		Description: "social toolbox",
		Mode:        TempToolModeToolbox,
		Credential:  "no_auth",
		Actions: []TempToolAction{{
			Name:        "reply_to_comment",
			Description: "reply to a comment",
			URLTemplate: "https://x.test/api/v1/comments/{comment_id}/replies", // WRONG url
			Method:      "POST",
			Params:      map[string]ToolParam{"comment_id": {Type: "string"}, "content": {Type: "string"}},
			Required:    []string{"comment_id", "content"},
		}},
	}
	store := map[string]TempTool{"moltbook": bundled}
	FindUserAgentTool = func(db Database, owner, name string) (TempTool, string, bool) {
		if tt, ok := store[name]; ok {
			return tt, "moltbook-agent", true
		}
		return TempTool{}, "", false
	}
	var wroteToAgent string
	AttachToolToAgent = func(db Database, owner, agentID string, tt TempTool) error {
		wroteToAgent = agentID
		store[tt.Name] = tt // reflect the write-back
		return nil
	}

	// Builder session: CanScopeGlobal true, but the tool isn't in its pools.
	sess := &ToolSession{
		Username:       "alice",
		ChatSessionID:  "builder-sess",
		DB:             &DBase{Store: kvlite.MemStore()},
		CanScopeGlobal: true,
	}

	// Sanity: Builder's own resolver can't see it (that's the whole gap).
	if _, ok := loadExistingToolRecord(sess, "moltbook"); ok {
		t.Fatal("precondition: moltbook must NOT be in Builder's own pools")
	}

	// Fix the reply_to_comment action's URL in place.
	res, err := updateGrouped(map[string]any{
		"name": "moltbook",
		"actions": []any{map[string]any{
			"name":         "reply_to_comment",
			"url_template": "https://x.test/api/v1/posts/{post_id}/comments",
			"params": map[string]any{
				"post_id": map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []any{"post_id", "content"},
		}},
	}, sess)
	if err != nil {
		t.Fatalf("in-place update failed: %v", err)
	}

	// It must have written back to the OWNING agent, not the user-wide pool.
	if wroteToAgent != "moltbook-agent" {
		t.Fatalf("edit should land on the owning agent, got %q", wroteToAgent)
	}
	if _, pooled := findInUserPool(sess, "moltbook"); pooled {
		t.Error("in-place edit must NOT promote the tool into the user-wide pool")
	}
	// And the redirect must be cleared after the call (no leak to later ops).
	if sess.BundleAuthoredToolTo != "" {
		t.Errorf("BundleAuthoredToolTo should be cleared after update, got %q", sess.BundleAuthoredToolTo)
	}
	// The fix actually took.
	got := store["moltbook"]
	var fixed bool
	for _, a := range got.Actions {
		if a.Name == "reply_to_comment" && strings.Contains(a.URLTemplate, "/posts/{post_id}/comments") {
			fixed = true
		}
	}
	if !fixed {
		t.Errorf("reply_to_comment URL was not updated; actions = %+v", got.Actions)
	}
	_ = res
}

func findInUserPool(sess *ToolSession, name string) (TempTool, bool) {
	for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
		if p.Tool.Name == name {
			return p.Tool, true
		}
	}
	return TempTool{}, false
}

// The tool namespace is unique per USER: authoring a name that another of the
// user's agents already holds is rejected, even though that other agent's tool
// isn't loaded in this session. This is what makes in-place edits unambiguous —
// a name resolves to exactly one tool across the whole user.
func TestCreateRejectsNameHeldByAnotherAgent(t *testing.T) {
	prev := ListUserAgentTools
	defer func() { ListUserAgentTools = prev }()
	ListUserAgentTools = func(db Database, owner string) []TempTool {
		return []TempTool{{Name: "moltbook", Mode: TempToolModeToolbox}}
	}
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "other-agent-sess",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	err := CheckCatalogNameCollision(sess, "moltbook", nil)
	if err == nil {
		t.Fatal("authoring a name another agent holds must be rejected (unique per-user namespace)")
	}
	if !strings.Contains(err.Error(), "taken") {
		t.Errorf("unexpected error: %v", err)
	}
	// A fresh name is fine.
	if err := CheckCatalogNameCollision(sess, "brand_new_tool", nil); err != nil {
		t.Errorf("a name no agent holds should be allowed: %v", err)
	}
}
