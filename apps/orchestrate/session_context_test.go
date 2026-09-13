package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A folded thread gets a context pill and a view with the numbers and the
// summary; a fresh one gets neither.
func TestContextPillAndViewFollowTheFoldState(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	msgs := make([]ChatMessage, 0, 30)
	for i := 0; i < 30; i++ {
		msgs = append(msgs, ChatMessage{Role: "user", Content: "m"})
	}
	sess, _ := saveChatSession(udb, ChatSession{AgentID: "agent-1", Title: "long", Messages: msgs})
	q := "?agent=agent-1&session=" + sess.ID
	call := func(path string, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := asUser(httptest.NewRequest(http.MethodGet, path, nil), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	// Fresh: the pill says nothing.
	w := call("/api/session-status"+q, T.handleSessionStatus)
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("fresh thread pill = %s", w.Body.String())
	}

	saveCompactState(udb, "agent-1", sess.ID, CompactState{Summary: "They discussed the Q3 plan.", SummarizedThrough: 18, FoldSeq: 2})
	w = call("/api/session-status"+q, T.handleSessionStatus)
	var pill map[string]any
	json.Unmarshal(w.Body.Bytes(), &pill)
	if pill["label"] != "context: 2 folds" || !strings.HasPrefix(pill["detail_url"].(string), "api/session-context?") {
		t.Fatalf("pill = %v", pill)
	}

	w = call("/api/session-context"+q, T.handleSessionContext)
	var view sessionContextView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Folds != 2 || view.Summarized != 18 || view.Verbatim != 12 || view.Stored != 30 || view.Summary != "They discussed the Q3 plan." {
		t.Fatalf("view = %+v", view)
	}
	if !strings.Contains(view.Note, "recall_history") {
		t.Fatalf("note should say where the folded text went: %q", view.Note)
	}
}

// The fold breadcrumb carries the numbers an owner can check against the view.
func TestFoldDiagDetail(t *testing.T) {
	before := CompactState{SummarizedThrough: 10, FoldSeq: 1}
	after := CompactState{SummarizedThrough: 18, FoldSeq: 2, Summary: strings.Repeat("s", 300)}
	d := foldDiagDetail(before, after, 30)
	for _, want := range []string{"fold #2", "8 older message(s)", "300 chars", "18 of 30"} {
		if !strings.Contains(d, want) {
			t.Fatalf("detail missing %q: %s", want, d)
		}
	}
}

// A machine thread's drawer carries the context section under the walk.
func TestMachineDrawerCarriesContext(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Triage", Start: "a", Phases: []MachinePhase{{Name: "a"}}})
	sess, _ := saveChatSession(udb, ChatSession{AgentID: "agent-1", Title: "t", MachineID: def.ID, Phase: "a",
		Messages: []ChatMessage{{Role: "user", Content: "x"}, {Role: "assistant", Content: "y"}}})
	saveCompactState(udb, "agent-1", sess.ID, CompactState{Summary: "earlier", SummarizedThrough: 1, FoldSeq: 1})
	r := asUser(httptest.NewRequest(http.MethodGet, "/api/session-state?agent=agent-1&session="+sess.ID, nil), user)
	w := httptest.NewRecorder()
	T.handleSessionState(w, r)
	var view sessionStateView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Context == nil || view.Context.Folds != 1 || view.Context.Summary != "earlier" {
		t.Fatalf("context section = %+v", view.Context)
	}
}
