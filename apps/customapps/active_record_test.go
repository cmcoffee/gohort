package customapps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A workbench posts the record it has open to chat/active; the co-author tool
// reads it back for that person. No body clears it; a malformed one is
// refused rather than clearing it by accident.
func TestTheWorkbenchRecordsItsOpenRecord(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "notes", Name: "Notes", Owner: "alice", RecordKey: "id", BodyField: "body", AgentID: "a1"}
	post := func(user, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/apps/notes/chat/active", strings.NewReader(body))
		return appAs(t, T, spec, user, r).Code
	}
	open := func(user string) string { return activeRecordID(T.recordBase(spec, user), "notes") }

	if code := post("alice", `{"id":" n-7 "}`); code != http.StatusNoContent {
		t.Fatalf("POST {id} answered %d", code)
	}
	if got := open("alice"); got != "n-7" {
		t.Errorf("the open record should read back trimmed: %q", got)
	}
	if code := post("bob", `{"id":"n-9"}`); code != http.StatusNoContent || open("bob") != "n-9" || open("alice") != "n-7" {
		t.Errorf("each person has their own open record: %d, bob %q, alice %q", code, open("bob"), open("alice"))
	}
	if code := post("alice", `{"id":`); code != http.StatusBadRequest || open("alice") != "n-7" {
		t.Errorf("a malformed body should be a 400 and change nothing: %d, %q", code, open("alice"))
	}
	if code := post("alice", ""); code != http.StatusNoContent || open("alice") != "" {
		t.Errorf("no body clears it: %d, %q", code, open("alice"))
	}
	if w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/notes/chat/active", nil)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be a 405, got %d", w.Code)
	}

	bare := AppSpec{Slug: "plain", Name: "Plain", Owner: "alice", RecordKey: "id"}
	if w := appAs(t, T, bare, "alice", httptest.NewRequest(http.MethodPost, "/apps/plain/chat/active", strings.NewReader(`{"id":"x"}`))); w.Code != http.StatusNotFound {
		t.Errorf("an app with no chat agent has no workbench chat: %d", w.Code)
	}
}
