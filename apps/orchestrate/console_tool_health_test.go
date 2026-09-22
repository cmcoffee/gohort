package orchestrate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An action that has failed past the threshold with no success shows in the
// pane with its count and last error; one that has worked once does not;
// Forget clears a tally and the pane follows.
func TestBrokenToolsPaneListsAndForgets(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	sess := &ToolSession{Username: user, DB: AuthDB()}
	for i := 0; i < neverWorkedThreshold; i++ {
		recordToolOutcome(sess, "jira", "create_issue", errors.New("401 unauthorized: bad token"))
	}
	// A flaky one: fails, then works — not broken.
	recordToolOutcome(sess, "jira", "search", errors.New("timeout"))
	recordToolOutcome(sess, "jira", "search", nil)

	call := func(method, path string, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := asUser(httptest.NewRequest(method, path, nil), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	w := call(http.MethodGet, "/api/console/broken-tools", T.handleConsoleBrokenTools)
	var rows []consoleBrokenToolRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Action != "jira - create_issue" || rows[0].ID != "jira.create_issue" {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.HasPrefix(rows[0].Failures, "5 failure(s)") || !strings.Contains(rows[0].Error, "bad token") || rows[0].Since == "" {
		t.Fatalf("row detail = %+v", rows[0])
	}

	w = call(http.MethodPost, "/api/console/broken-tools/forget?id=jira.create_issue", T.handleConsoleBrokenToolForget)
	if w.Code != http.StatusOK {
		t.Fatalf("forget: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/console/broken-tools", T.handleConsoleBrokenTools)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("after forget: %s", w.Body.String())
	}
	if w = call(http.MethodPost, "/api/console/broken-tools/forget?id=jira.create_issue", T.handleConsoleBrokenToolForget); w.Code != http.StatusNotFound {
		t.Fatalf("forgetting twice: %d", w.Code)
	}
	// The flaky action's tally survived the forget of its sibling.
	if got := loadToolOutcomes(AuthDB(), user); len(got) != 1 || got[0].Action != "search" || got[0].OK != 1 {
		t.Fatalf("sibling tally = %+v", got)
	}
}
