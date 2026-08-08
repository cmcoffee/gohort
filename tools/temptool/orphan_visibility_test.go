// tool_def searched six homes for a tool name and not the orphan pool, so a
// tool whose last carrying agent had been deleted answered "no tool found
// with name X" — indistinguishable from a name the user never created. The
// model's belief in the tool was correct; only its reachability had changed,
// and nothing said so. It went looking for another way in.
package temptool

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func orphanTestSession(t *testing.T) (*ToolSession, Database, string) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = prev })
	const user = "craig@example.com"
	return &ToolSession{Username: user, DB: db}, db, user
}

func TestGetFindsAnOrphanAndSaysItIsNotCallable(t *testing.T) {
	sess, db, user := orphanTestSession(t)
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool:            TempTool{Name: "get_top_stories", Description: "top news"},
		FormerAgentName: "Wren",
		OrphanedAt:      time.Now(),
	}})

	out, err := getGrouped(map[string]any{"name": "get_top_stories"}, sess)
	if err != nil {
		t.Fatalf("get errored on an orphan that plainly exists: %v", err)
	}
	if !strings.Contains(out, "ORPHANED") || !strings.Contains(out, "NOT callable") {
		t.Errorf("the state must be unmistakable:\n%s", out)
	}
	if !strings.Contains(out, "Wren") {
		t.Errorf("the deleted agent explains WHY it went dark:\n%s", out)
	}
	if !strings.Contains(out, "top news") {
		t.Errorf("the surviving definition must come back so it can be re-created:\n%s", out)
	}
}

// A live tool of the same name always wins the lookup — the orphan branch is
// last precisely so a rebuilt tool is never reported as dark.
func TestALiveToolBeatsItsOrphan(t *testing.T) {
	sess, db, user := orphanTestSession(t)
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool: TempTool{Name: "get_top_stories", Description: "old copy"},
	}})
	db.Set("persistent_temp_tools", user, []PersistentTempTool{
		{Tool: TempTool{Name: "get_top_stories", Description: "rebuilt"}},
	})

	out, err := getGrouped(map[string]any{"name": "get_top_stories"}, sess)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(out, "ORPHANED") || !strings.Contains(out, "rebuilt") {
		t.Errorf("the live copy must win:\n%s", out)
	}
}

func TestListSurfacesOrphansAsUncallable(t *testing.T) {
	sess, db, user := orphanTestSession(t)
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool:            TempTool{Name: "get_top_stories", Description: "top news"},
		FormerAgentName: "Wren",
	}})

	out, err := listGrouped(nil, sess)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "No temp tools defined") {
		t.Fatalf("an orphan-only pool still has something to report:\n%s", out)
	}
	if !strings.Contains(out, "ORPHANED") || !strings.Contains(out, "get_top_stories") {
		t.Errorf("the orphan must be listed:\n%s", out)
	}
	if !strings.Contains(out, "Wren") {
		t.Errorf("name the agent whose delete darkened it:\n%s", out)
	}
}

// Listing it twice — once as live, once as orphaned — would be its own kind
// of lie about what is callable.
func TestListDoesNotDoubleCountARebuiltTool(t *testing.T) {
	sess, db, user := orphanTestSession(t)
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool: TempTool{Name: "get_top_stories", Description: "old copy"},
	}})
	db.Set("persistent_temp_tools", user, []PersistentTempTool{
		{Tool: TempTool{Name: "get_top_stories", Description: "rebuilt"}},
	})

	out, err := listGrouped(nil, sess)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "ORPHANED") {
		t.Errorf("a name that is live again is not orphaned:\n%s", out)
	}
}
