package extensions

// A new personal API credential answers to the administrator's policy
// (core/governance): refused, held for approval, or live. One they already
// have is not adding one.

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
)

func TestANewCredentialFollowsThePolicy(t *testing.T) {
	app, req := authedExtensions(t)
	post := func(body string) int {
		r := req("POST", "/api/credentials")
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		app.handleCredentials(w, r)
		return w.Code
	}
	if code := post(`{"name":"kept","type":"bearer","base_url":"https://k.test","secret":"k"}`); code >= 300 {
		t.Fatalf("allowed by default: %d", code)
	}

	if err := governance.Set(governance.Credentials, string(governance.AdminOnly)); err != nil {
		t.Fatal(err)
	}
	if code := post(`{"name":"new","type":"bearer","base_url":"https://n.test","secret":"k"}`); code != 403 {
		t.Errorf("admin only should refuse a new one: %d", code)
	}
	if code := post(`{"name":"kept","type":"bearer","base_url":"https://k.test","description":"edited"}`); code >= 300 {
		t.Errorf("an edit to one they have was refused: %d", code)
	}

	if err := governance.Set(governance.Credentials, string(governance.NeedsApproval)); err != nil {
		t.Fatal(err)
	}
	if code := post(`{"name":"held","type":"bearer","base_url":"https://h.test","secret":"k"}`); code >= 300 {
		t.Fatalf("needs approval should still save it: %d", code)
	}
	c, ok := Secure().LoadUser("alice", "held")
	if !ok || !c.AwaitingApproval || !c.Disabled || !c.AdminDisabled {
		t.Fatalf("it should be held off, awaiting approval: %+v", c)
	}
	if err := Secure().SetDisabledOwned("alice", "held", false); err == nil || !strings.Contains(err.Error(), "waiting for an administrator to approve") {
		t.Errorf("the owner should be told it waits for approval, not that it was switched off: %v", err)
	}
	if err := Secure().AdminSetDisabledOwned("alice", "held", false); err != nil {
		t.Fatal(err)
	}
	if c, _ := Secure().LoadUser("alice", "held"); c.AwaitingApproval || c.Disabled {
		t.Errorf("an administrator's enable should approve it: %+v", c)
	}

	// A draft held for approval (Builder drafts them) stays held when the
	// person pastes its secret: the save works, the switch is the admin's.
	if err := Secure().SaveAPIDraft(SecureCredential{Name: "drafted", Type: SecureCredBearer, BaseURL: "https://d.test", Owner: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := Secure().HoldForApproval("alice", "drafted"); err != nil {
		t.Fatal(err)
	}
	if code := post(`{"name":"drafted","type":"bearer","base_url":"https://d.test","secret":"real"}`); code >= 300 {
		t.Fatalf("pasting the secret into a held draft failed: %d", code)
	}
	if c, _ := Secure().LoadUser("alice", "drafted"); !c.AwaitingApproval || !c.Disabled {
		t.Errorf("pasting the secret switched on a draft that waits for approval: %+v", c)
	}
}
