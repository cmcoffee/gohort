package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestToolCategorySetAndPreserved: Builder can categorize a tool at create time,
// and the category survives an unrelated update (round-trip trap) — so the
// user's tools group under their service/domain in the agent Tools picker.
func TestToolCategorySetAndPreserved(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "no_auth", Type: SecureCredNone, BaseURL: "https://x.example.com"}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}

	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "get_events", "description": "d", "mode": "api",
		"credential": "no_auth", "url_template": "https://x.example.com/e", "method": "GET",
		"category": "Apple Calendar",
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	rec, ok := loadExistingToolRecord(sess, "get_events")
	if !ok || rec.Category != "Apple Calendar" {
		t.Fatalf("category not set on create; got %q", rec.Category)
	}

	// Edit the description only — category must survive.
	if _, err := updateGrouped(map[string]any{"name": "get_events", "description": "d2"}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec, _ = loadExistingToolRecord(sess, "get_events")
	if rec.Category != "Apple Calendar" {
		t.Fatalf("category dropped on update round-trip; got %q", rec.Category)
	}
}
