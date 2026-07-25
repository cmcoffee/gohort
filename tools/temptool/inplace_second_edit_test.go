package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Flattened-namespace descendant of the Moltbook regression test. Originally
// this pinned "a second Builder edit must not fork an agent-scoped tool into
// the user pool" (the draft-shadow leak). Under the unified store there is no
// second home to fork into — the property becomes: repeated edits update the
// ONE record in place, its ScopeAgents restriction intact (an edit must never
// silently promote an agent-scoped tool to shared, nor grant it to the
// editing session's agent).
func TestSecondInPlaceEditStaysOnAgentRecord(t *testing.T) {
	sess := &ToolSession{
		Username:       "alice",
		ChatSessionID:  "builder-sess-2",
		DB:             &DBase{Store: kvlite.MemStore()},
		CanScopeGlobal: true, // Builder — the old leak needed this
	}
	seed := TempTool{
		Name:        "molt2",
		Description: "social toolbox",
		Mode:        TempToolModeToolbox,
		Credential:  "no_auth",
		Actions: []TempToolAction{{
			Name:        "reply",
			Description: "reply",
			URLTemplate: "https://x.test/api/v1/posts/{post_id}/comments",
			Method:      "POST",
			Params:      map[string]ToolParam{"post_id": {Type: "string"}, "content": {Type: "string"}},
			Required:    []string{"post_id", "content"},
		}},
	}
	if err := AdminPersistTempTool(sess.DB, sess.Username, seed); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if !SetUserToolScopeAgents(sess.DB, sess.Username, "molt2", []string{"moltbook-agent"}) {
		t.Fatal("seed scope failed")
	}

	edit := func(desc string) {
		if _, err := updateGrouped(map[string]any{
			"name": "molt2",
			"actions": []any{map[string]any{
				"name": "reply", "description": desc,
			}},
		}, sess); err != nil {
			t.Fatalf("edit %q: %v", desc, err)
		}
		row, ok := UserToolByName(sess.DB, sess.Username, "molt2")
		if !ok {
			t.Fatalf("edit %q: record vanished", desc)
		}
		if len(row.ScopeAgents) != 1 || row.ScopeAgents[0] != "moltbook-agent" {
			t.Fatalf("edit %q must preserve the agent scope (no promotion, no self-grant), got %v", desc, row.ScopeAgents)
		}
		if got := row.Tool.Actions[0].Description; got != desc {
			t.Fatalf("edit %q: the ONE record must carry the latest edit, got %q", desc, got)
		}
	}
	edit("reply (edit one)")
	edit("reply (edit two)")

	if all := LoadPersistentTempTools(sess.DB, sess.Username); len(all) != 1 {
		t.Fatalf("one name must remain ONE record, got %d", len(all))
	}
}
