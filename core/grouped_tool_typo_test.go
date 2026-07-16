package core

import (
	"strings"
	"testing"
)

// newTestPostTool builds a grouped tool mirroring moltbook's "post" action
// so we exercise the exact shape that sent the Builder agent into a
// re-send loop (submolt_name typed as submolta_name).
func newTestPostTool() *GroupedTool {
	gt := NewGroupedTool("moltbook", "test")
	gt.AddAction("post", &GroupedToolAction{
		Description: "Create a new post.",
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string", Description: "target submolt"},
			"title":        {Type: "string", Description: "post title"},
			"content":      {Type: "string", Description: "post body"},
			"client_id":    {Type: "string", Description: "idempotency key"},
			"type":         {Type: "string", Description: "post type"},
		},
		Required: []string{"submolt_name", "title", "client_id"},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return "ok", nil
		},
	})
	return gt
}

func TestGroupedTool_TypoOnRequiredParamIsNamed(t *testing.T) {
	gt := newTestPostTool()
	_, err := gt.Run(map[string]any{
		"action":        "post",
		"submolta_name": "general", // the typo that caused the loop
		"title":         "hi",
		"client_id":     "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	msg := err.Error()
	if !strings.Contains(msg, `you supplied "submolta_name"`) {
		t.Errorf("error should name the wrong key; got: %s", msg)
	}
	if !strings.Contains(msg, `did you mean "submolt_name"`) {
		t.Errorf("error should suggest the intended param; got: %s", msg)
	}
}

func TestGroupedTool_PlainMissingHasNoTypoHint(t *testing.T) {
	gt := newTestPostTool()
	// submolt_name simply omitted (no near-miss key supplied) — the
	// message must NOT invent a "did you mean".
	_, err := gt.Run(map[string]any{
		"action":    "post",
		"title":     "hi",
		"client_id": "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no near-miss key was supplied; should not suggest one; got: %s", err.Error())
	}
}

func TestGroupedTool_UnrelatedExtraKeyNotFlagged(t *testing.T) {
	// A genuinely unrelated stray key (not a typo of any param) shouldn't
	// produce a spurious suggestion — only the real missing-param message.
	def := &GroupedToolAction{
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string"},
			"title":        {Type: "string"},
		},
	}
	if got := def.nearestParamName("xyzzy"); got != "" {
		t.Errorf("unrelated key matched %q; want no suggestion", got)
	}
	// Transposition-style typo should still resolve.
	if got := def.nearestParamName("titel"); got != "title" {
		t.Errorf("titel should suggest title; got %q", got)
	}
}
