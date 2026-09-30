package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// accessFixture is a deployment credential limited to bob, pointed at a test
// server that answers 200, on a fresh store behind Secure().
func accessFixture(t *testing.T) (*SecureAPI, string) {
	t.Helper()
	store := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return store }
	t.Cleanup(func() { AuthDB = prev })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	t.Cleanup(srv.Close)
	s := Secure()
	if err := s.Save(SecureCredential{Name: "team", Type: SecureCredBearer, BaseURL: srv.URL, AllowedUsers: []string{"bob"}}, "team-key"); err != nil {
		t.Fatal(err)
	}
	return s, srv.URL
}

// Who may spend a key is asked in dispatch, where every path meets: a user
// the credential is not shared with is refused, and so is a call that runs as
// no one; the person it is shared with gets through; an agent that switched
// the credential off is refused even for that person.
func TestDispatchAsksWhoMaySpendTheKey(t *testing.T) {
	s, url := accessFixture(t)
	call := func(sess *ToolSession) error {
		_, err := s.DispatchToolCall(sess, "team", url+"/x", "GET", "")
		return err
	}
	if err := call(&ToolSession{Username: "alice"}); err == nil || !strings.Contains(err.Error(), "not shared") {
		t.Errorf("alice is not given the key: %v", err)
	}
	if err := call(nil); err == nil || !strings.Contains(err.Error(), "runs as no one") {
		t.Errorf("a call with no user reaches only open keys: %v", err)
	}
	if err := call(&ToolSession{Username: "bob"}); err != nil {
		t.Errorf("bob is: %v", err)
	}
	if err := call(&ToolSession{Username: "bob", DeniedCredentials: map[string]bool{"team": true}}); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("an agent that switched it off is refused: %v", err)
	}
}

// The fetch_url_<cred> tools are offered only for keys the user may spend,
// and each call resolves the credential again: a key restricted after the
// tools were built is refused on the next call, not the next turn.
func TestCredentialToolsFollowAccess(t *testing.T) {
	s, url := accessFixture(t)
	has := func(user string) bool {
		for _, td := range s.BuildTools(&ToolSession{Username: user}) {
			if td.Tool.Name == "fetch_url_team" {
				return true
			}
		}
		return false
	}
	if has("alice") || !has("bob") {
		t.Fatalf("offered to bob only: alice=%v bob=%v", has("alice"), has("bob"))
	}
	var tool AgentToolDef
	for _, td := range s.BuildTools(&ToolSession{Username: "bob"}) {
		if td.Tool.Name == "fetch_url_team" {
			tool = td
		}
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"url": url + "/x"}); err != nil {
		t.Fatalf("bob's call: %v", err)
	}
	c, _ := s.Load("team")
	c.AllowedUsers = []string{"carol"}
	if err := s.Save(c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"url": url + "/x"}); err == nil {
		t.Error("taken from bob mid-conversation: his next call is refused")
	}
}

// A plain fetch to a host covered by a key the user may not spend goes out
// without it, rather than being routed to that key.
func TestAutoRouteSkipsKeysTheUserMayNotSpend(t *testing.T) {
	s, url := accessFixture(t)
	if name, err := s.AutoRouteCredential(url+"/x", "alice"); name != "" || err != nil {
		t.Errorf("alice's fetch is not routed to bob's key: %q %v", name, err)
	}
	if name, _ := s.AutoRouteCredential(url+"/x", "bob"); name != "team" {
		t.Errorf("bob's is: %q", name)
	}
}

// A standing poll is held to what its owner may spend, when it is made and on
// every fire, and a SECURED credential cannot be polled at all.
func TestStandingPollsRunAsTheirOwner(t *testing.T) {
	s, url := accessFixture(t)
	if err := s.PollMayUse("alice", "team"); err == nil {
		t.Error("alice may not poll bob's key")
	}
	if err := s.PollMayUse("bob", "team"); err != nil {
		t.Errorf("bob may: %v", err)
	}
	args := map[string]any{"url": url + "/x"}
	if _, err := InvokeWatchTool("alice", "", "call_team", args); err == nil {
		t.Error("a poll owned by alice cannot fire through bob's key")
	}
	if _, err := InvokeWatchTool("bob", "", "call_team", args); err != nil {
		t.Errorf("bob's fires: %v", err)
	}
	if _, err := InvokeWatcherTool("call_team", args); err == nil {
		t.Error("with no owner a poll reaches only open keys")
	}
	c, _ := s.Load("team")
	c.Secured = true
	s.Save(c, "")
	s.SetSecured("team", true)
	if err := s.PollMayUse("bob", "team"); err == nil || !strings.Contains(err.Error(), "SECURED") {
		t.Errorf("a secured key cannot be polled: %v", err)
	}
	if _, err := InvokeWatchTool("bob", "", "call_team", args); err == nil || !strings.Contains(err.Error(), "SECURED") {
		t.Errorf("nor fire: %v", err)
	}
}
