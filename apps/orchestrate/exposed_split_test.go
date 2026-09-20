package orchestrate

// Exposed used to mean two things at once: everybody may use this, AND put a
// card on the dashboard. The public surface needed an access gate and the app
// grant happened to be one, so the two welded together — which meant an owner
// could not add a shortcut without widening who could use the agent, or widen
// it without putting a card in front of people who never asked for one.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A record written before the split keeps behaving exactly as it did: it was
// usable by everybody and it had a card, which is both halves set.
func TestAPublishedRecordMigratesToBothHalves(t *testing.T) {
	got := migrateExposedFlag(AgentRecord{ID: "a1", Exposed: true})
	if !got.Everyone || !got.ShowOnDashboard {
		t.Errorf("the split lost a half: %+v", got)
	}
	// And the old flag is cleared, so the migration is idempotent rather than
	// re-deciding on every read.
	if got.Exposed {
		t.Error("the retired flag survived the migration")
	}
	// An already-split record is untouched, including one the owner has
	// deliberately narrowed to a shortcut with no reach.
	shortcut := AgentRecord{ID: "a2", ShowOnDashboard: true}
	if out := migrateExposedFlag(shortcut); out.Everyone {
		t.Errorf("a shortcut-only agent was widened by the migration: %+v", out)
	}
}

// The reach is an administrator's to grant; the shortcut is nobody's business
// but the owner's. A PATCH used to apply either directly, so somebody who
// could not flip the toggle on the privileges card could publish to the whole
// deployment through the other door.
func TestPatchCannotPublishWithoutApproval(t *testing.T) {
	T, udb, _ := newTestOrchestrate(t)
	rec, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Wren",
		OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { requestIsAdminAgent = RequestIsAdmin })

	r := httptest.NewRequest(http.MethodPatch, "/api/agents/a1",
		strings.NewReader(`{"exposed":true}`))
	T.patchAgent(httptest.NewRecorder(), r, udb, "alice", rec.ID)

	got, _ := loadAgent(udb, rec.ID)
	if got.Everyone {
		t.Error("a PATCH published the agent without an administrator")
	}
	// The shortcut takes the same door and IS applied, because it grants
	// nothing.
	r = httptest.NewRequest(http.MethodPatch, "/api/agents/a1",
		strings.NewReader(`{"show_on_dashboard":true}`))
	T.patchAgent(httptest.NewRecorder(), r, udb, "alice", rec.ID)
	if got, _ := loadAgent(udb, rec.ID); !got.ShowOnDashboard {
		t.Error("a shortcut was refused; it grants nothing and needs nobody")
	}
}

// Hiding an agent from the fleet list leaves its owner no way to open it, so
// the save path adds a SHORTCUT. It must not also widen who can use it — that
// was the old flag doing two things, and the default only ever meant one.
func TestHidingAnAgentAddsAShortcutNotReach(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	saved, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Helper",
		OrchestratorPrompt: "x", Hidden: true})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.ShowOnDashboard {
		t.Error("a hidden agent got no surface to reach it")
	}
	if saved.Everyone {
		t.Error("hiding an agent published it to the whole deployment")
	}
}
