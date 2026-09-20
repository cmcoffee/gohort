package orchestrate

// Publishing an agent reaches every signed-in user of the deployment, which is
// the same reach a shared app has and has needed an administrator since
// v0.6.710. An agent is the larger grant: it carries its owner's tools,
// credentials and memory, and every opener runs it as the owner.
//
// Before this, any signed-in user could flip the flag on their own agent and it
// was live. The admin governance page had listed "agent" as a promotion kind
// all along with nothing able to file one.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

func publishFlags(t *testing.T, T *OrchestrateApp, user, agentID string, flags map[string]bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"agent_id": agentID, "flags": flags})
	r := asUser(httptest.NewRequest(http.MethodPost, "/api/console/privileges", bytes.NewReader(body)), user)
	w := httptest.NewRecorder()
	T.handleConsolePrivileges(w, r)
	return w
}

func TestPublishingAnAgentIsRequestedNotApplied(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	auth := &DBase{Store: kvlite.MemStore()}
	savedAuth, savedAdmin := AuthDB, requestIsAdminAgent
	AuthDB = func() Database { return auth }
	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { AuthDB, requestIsAdminAgent = savedAuth, savedAdmin })

	rec, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Helper", OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := publishFlags(t, T, user, rec.ID, map[string]bool{"exposed": true})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "exposed") {
		t.Fatalf("the caller was not told it is a request: %d %s", w.Code, w.Body.String())
	}
	// Not live. A capability shown as on while an administrator has not looked
	// at it is worse than one that plainly says it is waiting.
	if got, _ := loadAgent(udb, rec.ID); got.Exposed {
		t.Error("the agent was published without an administrator")
	}
	if len(promotion.ListPromotionRequests(auth, true)) != 1 {
		t.Fatal("nothing landed on the administrator's queue")
	}

	// Approving is what publishes it, through the registered approver.
	registerAgentPromotion(T)
	for _, req := range promotion.ListPromotionRequests(auth, true) {
		if err := promotion.Approve(auth, req.ID, "root"); err != nil {
			t.Fatalf("approve: %v", err)
		}
	}
	if got, _ := loadAgent(udb, rec.ID); !got.Exposed {
		t.Error("approval did not publish the agent")
	}
}

// Turning it OFF is the owner's, always. Nobody needs permission to stop
// publishing, and requiring it would mean an owner who wanted something down
// had to wait for somebody else.
func TestUnpublishingNeedsNobody(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	auth := &DBase{Store: kvlite.MemStore()}
	savedAuth, savedAdmin := AuthDB, requestIsAdminAgent
	AuthDB = func() Database { return auth }
	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { AuthDB, requestIsAdminAgent = savedAuth, savedAdmin })

	rec, _ := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Helper", OrchestratorPrompt: "help", Exposed: true})
	if w := publishFlags(t, T, user, rec.ID, map[string]bool{"exposed": false}); w.Code != http.StatusNoContent {
		t.Fatalf("unpublish was not applied directly: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, rec.ID); got.Exposed {
		t.Error("the agent is still published")
	}
	if len(promotion.ListPromotionRequests(auth, true)) != 0 {
		t.Error("taking something down filed a request")
	}
}

// An admin acts directly, the same way they do on an app share: they are the
// one the request would have gone to.
func TestAnAdminPublishesDirectly(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	auth := &DBase{Store: kvlite.MemStore()}
	savedAuth, savedAdmin := AuthDB, requestIsAdminAgent
	AuthDB = func() Database { return auth }
	requestIsAdminAgent = func(*http.Request) bool { return true }
	t.Cleanup(func() { AuthDB, requestIsAdminAgent = savedAuth, savedAdmin })

	rec, _ := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Helper", OrchestratorPrompt: "help"})
	if w := publishFlags(t, T, user, rec.ID, map[string]bool{"exposed": true}); w.Code != http.StatusNoContent {
		t.Fatalf("an admin was made to ask: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, rec.ID); !got.Exposed {
		t.Error("the admin's own toggle did not take")
	}
}

// Both doors are the same decision: whether this person's agent may be reached
// by everybody. The kind carries which one so an administrator does not have to
// read two near-identical rows and work out the difference.
func TestMCPExposureGoesThroughTheSameGate(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	auth := &DBase{Store: kvlite.MemStore()}
	savedAuth, savedAdmin := AuthDB, requestIsAdminAgent
	AuthDB = func() Database { return auth }
	requestIsAdminAgent = func(*http.Request) bool { return false }
	t.Cleanup(func() { AuthDB, requestIsAdminAgent = savedAuth, savedAdmin })

	rec, _ := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Helper", OrchestratorPrompt: "help"})
	publishFlags(t, T, user, rec.ID, map[string]bool{"mcp_exposed": true})
	if got, _ := loadAgent(udb, rec.ID); got.MCPExposed {
		t.Error("MCP exposure skipped the gate; it takes the agent OUTSIDE the deployment")
	}
	reqs := promotion.ListPromotionRequests(auth, true)
	if len(reqs) != 1 || !strings.Contains(reqs[0].Name, "mcp_exposed") {
		t.Fatalf("the request does not say which door it is for: %+v", reqs)
	}
	registerAgentPromotion(T)
	if err := promotion.Approve(auth, reqs[0].ID, "root"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, _ := loadAgent(udb, rec.ID)
	if !got.MCPExposed {
		t.Error("approving the MCP request did not open that door")
	}
	if got.Exposed {
		t.Error("approving MCP also published to the dashboard; they are separate doors")
	}
}
