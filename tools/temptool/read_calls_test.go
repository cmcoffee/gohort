package temptool

import (
	"slices"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An action marked as one that only reads, on a toolbox built on its author's
// own credential, becomes one of that credential's read calls, made and when
// another action is edited. On somebody else's credential (one lent to them)
// the mark stays a label: it never reaches the key.
func TestAReadMarkOnYourOwnKeyBecomesOneOfItsReadCalls(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "jira", Type: SecureCredNone, Owner: "alice", BaseURL: "https://jira.example"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "carol", BaseURL: "https://wiki.example"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := Secure().SetCredentialShares("carol", "wiki", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
	box := func(name, cred string) map[string]any {
		return map[string]any{"name": name, "description": "d", "mode": "toolbox", "credential": cred, "actions": []any{
			map[string]any{"name": "search", "description": "Search.", "url_template": "https://x.example/rest/api/3/search/jql?limit=5", "method": "POST",
				"body_template": `{"jql": {jql}}`, "params": map[string]any{"jql": map[string]any{"type": "string"}}, "reads": true},
			map[string]any{"name": "comment", "description": "Comment.", "url_template": "https://x.example/rest/api/3/issue/{key}/comment", "method": "POST",
				"body_template": `{"body": {body}}`, "params": map[string]any{"key": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}}},
		}}
	}
	if _, err := createGrouped(box("jira_tools", "jira"), sess); err != nil {
		t.Fatal(err)
	}
	c, _ := Secure().LoadUser("alice", "jira")
	if !slices.Equal(c.ReadCalls, []string{"POST /rest/api/3/search/jql"}) {
		t.Fatalf("read calls after the create: %v", c.ReadCalls)
	}
	if _, err := updateGrouped(map[string]any{"name": "jira_tools", "actions": []any{map[string]any{"name": "view", "description": "View.",
		"url_template": "https://x.example/rest/api/3/issue/{key}/view", "method": "POST", "body_template": `{}`,
		"params": map[string]any{"key": map[string]any{"type": "string"}}, "reads": true}}}, sess); err != nil {
		t.Fatal(err)
	}
	c, _ = Secure().LoadUser("alice", "jira")
	if !slices.Equal(c.ReadCalls, []string{"POST /rest/api/3/search/jql", "POST /rest/api/3/issue/*/view"}) {
		t.Errorf("read calls after an update: %v", c.ReadCalls)
	}

	if _, err := createGrouped(box("wiki_tools", "wiki"), sess); err != nil {
		t.Fatal(err)
	}
	if w, _ := Secure().LoadUser("carol", "wiki"); len(w.ReadCalls) != 0 {
		t.Errorf("a read mark on a toolbox over a lent key reached the key: %v", w.ReadCalls)
	}
}

func TestReadCallPath(t *testing.T) {
	for tpl, want := range map[string]string{
		"https://jira.example/rest/api/3/search/jql?x=1": "/rest/api/3/search/jql",
		"{base}/issue/{key}/comment":                     "/*/issue/*/comment",
		"https://jira.example/issue-{id}/view":           "/*/view",
		"https://jira.example":                           "",
	} {
		if got := readCallPath(tpl); got != want {
			t.Errorf("readCallPath(%q) = %q, want %q", tpl, got, want)
		}
	}
}
