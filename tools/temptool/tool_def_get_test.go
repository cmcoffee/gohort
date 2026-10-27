package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A tool a colleague lent, and the user added, rides in the user's session like
// any other. Updating it resolved that session copy and persisted the edit into
// the EDITOR's pool: the owner's tool never changed, and the new own copy
// shadowed it from then on. It is refused, and says whose it is.
func TestUpdatingSomebodyElsesToolIsRefused(t *testing.T) {
	withMemRootDB(t)
	lent := TempTool{Name: "wiki_read", Description: "lent", CommandTemplate: "echo lent"}
	if err := AdminPersistTempTool(RootDB, "lender", lent); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "lender", "wiki_read", []string{"taker"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(RootDB, "taker", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "taker", DB: RootDB, ChatSessionID: "s1", CanScopeGlobal: true}
	live := lent
	if err := sess.AppendTempTool(&live); err != nil {
		t.Fatal(err)
	}

	res, err := updateGrouped(map[string]any{"name": "wiki_read", "description": "edited"}, sess)
	if err != nil {
		t.Fatalf("a refusal is a result, not an error: %v", err)
	}
	if !strings.Contains(res, "belongs to lender") || !strings.Contains(res, "NEW name") {
		t.Errorf("the refusal should say whose it is and how to make one's own: %s", res)
	}
	for _, p := range LoadPersistentTempTools(RootDB, "taker") {
		if p.Tool.Name == "wiki_read" {
			t.Fatal("the update forked the lent tool into the editor's pool")
		}
	}
	if got, _ := getGrouped(map[string]any{"name": "wiki_read"}, sess); !strings.Contains(got, "owned by lender") {
		t.Errorf("get should say whose the live copy is: %s", got)
	}

	// A tool the deployment publishes and the user never added is refused the
	// same way (it used to be the only case caught, and only when nothing else
	// resolved the name).
	if err := AdminPersistTempTool(RootDB, "publisher", TempTool{Name: "jira_find", Description: "d", CommandTemplate: "echo j"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(RootDB, "publisher", "jira_find", true); err != nil {
		t.Fatal(err)
	}
	if res, _ := updateGrouped(map[string]any{"name": "jira_find", "description": "edited"}, sess); !strings.Contains(res, "belongs to publisher") {
		t.Errorf("a published tool somebody else owns should be refused: %s", res)
	}

	// The user's OWN tool of a lent name is theirs to edit.
	if err := AdminPersistTempTool(RootDB, "taker", TempTool{Name: "wiki_read", Description: "mine", CommandTemplate: "echo mine"}); err != nil {
		t.Fatal(err)
	}
	if owner, _ := foreignToolOwner(sess, "wiki_read"); owner != "" {
		t.Errorf("the user's own copy was treated as %s's", owner)
	}
}
