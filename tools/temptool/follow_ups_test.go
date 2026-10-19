package temptool

// A toolbox action declares the other actions its reply asks for as the
// service's own next step (follow_ups). Held to actions of the same toolbox,
// kept through an update of another action, and carried onto the runtime
// tool for the injection scan with each action's endpoint path.

import (
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func postingToolbox(followUps any) map[string]any {
	post := map[string]any{"name": "create_post", "description": "Post.", "url_template": "https://social.example/api/v1/posts", "method": "POST",
		"body_template": `{"text": {text}}`, "params": map[string]any{"text": map[string]any{"type": "string"}}}
	if followUps != nil {
		post["follow_ups"] = followUps
	}
	return map[string]any{"name": "social", "description": "d", "mode": "toolbox", "actions": []any{
		post,
		map[string]any{"name": "verify_post", "description": "Verify.", "url_template": "https://social.example/api/v1/verify?code={code}", "method": "POST",
			"params": map[string]any{"code": map[string]any{"type": "string"}}},
		map[string]any{"name": "get_feed", "description": "Feed.", "url_template": "https://social.example/api/v1/feed"},
	}}
}

func TestAnActionDeclaresTheStepsItsReplyAsksFor(t *testing.T) {
	sess := &ToolSession{Username: "alice"}
	for _, bad := range []struct {
		follow any
		says   string
	}{{[]any{"not_an_action"}, "not an action of this toolbox"}, {[]any{"create_post"}, "names the action itself"}} {
		if _, err := createGrouped(postingToolbox(bad.follow), sess); err == nil || !strings.Contains(err.Error(), bad.says) {
			t.Errorf("follow_ups %v should be refused (%s): %v", bad.follow, bad.says, err)
		}
	}
	if _, err := createGrouped(postingToolbox([]any{"verify_post", "verify_post"}), sess); err != nil {
		t.Fatal(err)
	}
	// An update to another action keeps it.
	if _, err := updateGrouped(map[string]any{"name": "social", "actions": []any{map[string]any{"name": "get_feed", "description": "The feed."}}}, sess); err != nil {
		t.Fatal(err)
	}
	tt := sess.LookupTempTool("social")
	var post TempToolAction
	for _, a := range tt.Actions {
		if a.Name == "create_post" {
			post = a
		}
	}
	if !slices.Equal(post.FollowUps, []string{"verify_post"}) {
		t.Fatalf("follow_ups after an update of another action: %v", post.FollowUps)
	}
	def := agentToolFromTemp(sess, tt)
	if !slices.Equal(def.Tool.FollowUps["create_post"], []string{"verify_post"}) || def.Tool.ActionPaths["verify_post"] != "/api/v1/verify" {
		t.Errorf("the runtime tool carries follow-ups %v and paths %v", def.Tool.FollowUps, def.Tool.ActionPaths)
	}
	if _, declares := def.Tool.FollowUps["get_feed"]; declares {
		t.Error("an action that declares nothing was given a step")
	}
}

// The every-action form: one named step that the reply to any action may ask
// for, and nothing else, so a feed can at most get the agent to try that one.
func TestEveryActionsReplyMayAskForTheOneNamedStep(t *testing.T) {
	sess := &ToolSession{Username: "alice"}
	bad := postingToolbox(nil)
	bad["follow_ups_from_any"] = []any{"nowhere"}
	if _, err := createGrouped(bad, sess); err == nil || !strings.Contains(err.Error(), "not an action of this toolbox") {
		t.Errorf("an every-action step that is not an action should be refused: %v", err)
	}
	args := postingToolbox(nil)
	args["follow_ups_from_any"] = []any{"verify_post"}
	if _, err := createGrouped(args, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := updateGrouped(map[string]any{"name": "social", "actions": []any{map[string]any{"name": "get_feed", "description": "The feed."}}}, sess); err != nil {
		t.Fatal(err)
	}
	tt := sess.LookupTempTool("social")
	if !slices.Equal(tt.FollowUpsFromAny, []string{"verify_post"}) {
		t.Fatalf("the every-action step did not survive an update: %v", tt.FollowUpsFromAny)
	}
	def := agentToolFromTemp(sess, tt)
	for _, from := range []string{"create_post", "get_feed"} {
		if got := def.Tool.FollowUps[from]; !slices.Equal(got, []string{"verify_post"}) {
			t.Errorf("%s's reply may ask for verify_post and nothing else: %v", from, got)
		}
	}
	if got, ok := def.Tool.FollowUps["verify_post"]; ok {
		t.Errorf("verify_post was made a step of itself: %v", got)
	}
}
