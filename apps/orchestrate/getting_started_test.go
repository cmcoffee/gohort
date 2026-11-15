package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/gettingstarted"
)

// The Agents step is for whoever can open Agents, and done once they own an
// agent of their own: the same test that sends a newcomer to the first-run
// wizard, whose door the step links to.
func TestTheAssistantStepIsDoneWithAnAgentOfYourOwn(t *testing.T) {
	_, udb, user := newTestOrchestrate(t)
	registerAssistantStep()
	AuthSetUser(AuthDB(), user, "pw-alice-123", true)
	r := asUser(httptest.NewRequest(http.MethodGet, "/", nil), user)
	step := func() (gettingstarted.Item, bool) {
		for _, it := range gettingstarted.For(r, user) {
			if it.ID == "assistant" {
				return it, true
			}
		}
		return gettingstarted.Item{}, false
	}
	it, ok := step()
	if !ok || it.Finished || it.Link != orchPath("") {
		t.Fatalf("with no agent of their own: %+v %v", it, ok)
	}
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := step(); !it.Finished {
		t.Error("with an agent of their own the step is not done")
	}
}
