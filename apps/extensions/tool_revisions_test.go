package extensions

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A tool's kept versions are listed, previewed and restored through its own
// route, for its owner; somebody else's tool of the same name is not theirs.
func TestAToolsHistoryIsListedPreviewedAndRestored(t *testing.T) {
	app, req := authedExtensions(t)
	tool := func(url string) TempTool {
		return TempTool{Name: "jira_search", Mode: TempToolModeAPI, CommandTemplate: url, Method: "GET"}
	}
	if err := AdminPersistTempTool(AuthDB(), "alice", tool("https://jira.example/rest/api/2/search")); err != nil {
		t.Fatal(err)
	}
	if !UpdatePersistentTempTool(AuthDB(), "alice", tool("https://jira.example/rest/api/3/search")) {
		t.Fatal("update")
	}
	if err := AdminPersistTempTool(AuthDB(), "bob", tool("https://bob.example/one")); err != nil {
		t.Fatal(err)
	}
	UpdatePersistentTempTool(AuthDB(), "bob", tool("https://bob.example/two"))

	w := httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools/jira_search/revisions"))
	var got struct {
		Entries []struct {
			Detail string `json:"detail"`
		} `json:"entries"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Entries) != 1 {
		t.Fatalf("the list: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools/jira_search/revisions/preview?rev=1"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "api/2/search") || strings.Contains(w.Body.String(), "bob.example") {
		t.Fatalf("the preview: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("POST", "/api/tools/jira_search/revisions/restore?rev=1"))
	if w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if p, _ := UserToolByName(AuthDB(), "alice", "jira_search"); p.Tool.CommandTemplate != "https://jira.example/rest/api/2/search" {
		t.Errorf("after the restore: %q", p.Tool.CommandTemplate)
	}
	if p, _ := UserToolByName(AuthDB(), "bob", "jira_search"); p.Tool.CommandTemplate != "https://bob.example/two" {
		t.Errorf("restoring alice's tool touched bob's: %q", p.Tool.CommandTemplate)
	}
	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/tools/nobodys_tool/revisions"))
	if w.Code == 200 {
		t.Error("a tool the caller does not have answered with a history")
	}
}
