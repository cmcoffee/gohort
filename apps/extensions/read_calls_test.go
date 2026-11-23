package extensions

import (
	"net/http/httptest"
	"slices"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The credential's "Calls that only read" saves one call per line onto the
// owner's own credential and reads back as it was written; a line that is not
// a call is refused.
func TestACredentialsCallsThatOnlyReadSaveFromTheForm(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: "jira", Type: SecureCredBearer, Owner: "alice", BaseURL: "https://jira.example"}, "k"); err != nil {
		t.Fatal(err)
	}
	T := &Extensions{}
	call := &jsonapi.Call{User: "alice", R: httptest.NewRequest("POST", "/api/credentials?action=read_calls&name=jira", nil)}
	if _, err := T.handleReadCallsSave(call, struct {
		ReadCalls string `json:"read_calls"`
	}{"POST /rest/api/3/search/jql\n\n  post /rest/api/3/issue/*/view  \n"}); err != nil {
		t.Fatal(err)
	}
	c, _ := Secure().LoadUser("alice", "jira")
	if !slices.Equal(c.ReadCalls, []string{"POST /rest/api/3/search/jql", "POST /rest/api/3/issue/*/view"}) {
		t.Fatalf("saved %v", c.ReadCalls)
	}
	if v := readCallsView(c)["read_calls"]; v != "POST /rest/api/3/search/jql\nPOST /rest/api/3/issue/*/view" {
		t.Errorf("the form reads back %q", v)
	}
	if _, err := T.handleReadCallsSave(call, struct {
		ReadCalls string `json:"read_calls"`
	}{"search"}); err == nil {
		t.Error("a line that is not a call was taken")
	}
}
