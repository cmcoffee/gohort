package extensions

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// The three choices against the real tool scope (orchestrate's provider),
// with a sub-agent among the agents: every click the panel can make goes
// through, in any order.
func TestToolWhoAgainstTheRealScope(t *testing.T) {
	app, req := authedExtensions(t)
	db := AuthDB()
	agents := UserDB(db.Bucket("orchestrate"), "alice")
	for _, a := range []orchestrate.AgentRecord{
		{ID: "a-writer", Name: "Writer", Owner: "alice", OrchestratorPrompt: "p"},
		{ID: "a-critic", Name: "Critic", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-writer", InheritParentTools: true},
		{ID: "a-other", Name: "Other", Owner: "alice", OrchestratorPrompt: "p"},
	} {
		agents.Set("orchestrate_agents", a.ID, a)
	}
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "make_event", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) {
		t.Helper()
		r := req("POST", "/api/tool-access?section=who&name=make_event")
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		serve(app)(w, r)
		if w.Code != 204 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	steps := []string{
		`{"mode":"agents"}`,
		`{"agent":"a-critic","on":true}`,
		`{"agent":"a-writer","on":true}`,
		`{"agent":"a-critic","on":false}`,
		`{"mode":"all"}`,
		`{"mode":"agents"}`,
		`{"agent":"a-critic","on":true}`,
		`{"agent":"a-writer","on":false}`,
		`{"agent":"a-critic","on":false}`,
		`{"agent":"a-other","on":true}`,
		`{"mode":"bound"}`,
		`{"mode":"agents"}`,
		`{"mode":"all"}`,
		`{"agent":"a-critic","on":false}`,
	}
	for _, s := range steps {
		post(s)
	}
}
