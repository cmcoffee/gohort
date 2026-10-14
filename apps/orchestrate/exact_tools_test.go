package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// An app agent whose spec says ExactTools is offered its host app's tools,
// its AllowedTools and the two ask tools on a web turn, and nothing the
// framework adds to every other turn; its prompt names none of the framework
// tools it was not given. The same agent without the flag gets the usual
// catalog, so the test is measuring the flag.
func TestAnExactToolsAppAgentIsOfferedOnlyItsOwnTools(t *testing.T) {
	for _, exact := range []bool{true, false} {
		T, udb, user := newTestOrchestrate(t)
		pinRootDB(t)
		fake := &FakeLLM{Turns: []FakeTurn{{Content: "Done."}}}
		T.LLM = fake
		id := "app-test-exact-publisher"
		if !exact {
			id = "app-test-open-publisher"
		}
		appagents.RegisterAppAgent(appagents.AppAgentSpec{
			ID: id, OwningApp: "Test", Name: "Publisher", Prompt: "You publish one document.",
			AllowedTools: []string{"ask_user", "ask_user_form"}, Hidden: true, ExactTools: exact,
		})
		agent, ok := loadAgent(udb, id)
		if !ok {
			t.Fatalf("app agent %s did not resolve", id)
		}
		appTools := []AgentToolDef{{
			Tool: Tool{Name: "publish_document", Description: "Publish the open document."},
			Handler: func(context.Context, map[string]any) (string, error) {
				return "Published.", nil
			},
		}}
		body, _ := json.Marshal(map[string]any{"message": "Publish this guide."})
		w := httptest.NewRecorder()
		r := asUser(httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+id, strings.NewReader(string(body))), user)
		T.handleSendWithAppTools(w, r, udb, user, agent, appTools)

		if fake.Calls() == 0 {
			t.Fatalf("exact=%v: the turn never reached the model: %s", exact, w.Body.String())
		}
		var names []string
		for _, td := range fake.Config(0).Tools {
			names = append(names, td.Name)
		}
		sort.Strings(names)
		got := strings.Join(names, ",")
		if exact {
			if got != "ask_user,ask_user_form,publish_document" {
				t.Errorf("an ExactTools agent was offered %s", got)
			}
			p := fake.Prompt(0)
			for _, absent := range []string{"plan_set", "request_build", "find_tools"} {
				if strings.Contains(p, absent) {
					t.Errorf("an ExactTools agent's prompt names %s, which it was not given", absent)
				}
			}
		} else if len(names) <= 3 {
			t.Errorf("the same agent without ExactTools was offered only %s: the test is not measuring the flag", got)
		}
	}
}
