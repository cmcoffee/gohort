package core

// The per-user connect URL has to work from wherever it is rendered.
//
// It was account-relative ("mcp/connect?server=X") and its only
// consumer is the connections card on the EXTENSIONS page, where it
// resolved to /mcp/connect — a route nothing serves. The popup then
// loaded a page whose own relative sources resolved against /mcp/, so
// every table on it fetched HTML instead of JSON. What that looked like
// from outside: Reconnect landed on the credentials list and said
// "records.filter is not a function".

import (
	"os"
	"strings"
	"testing"
)

func TestAPerUserConnectURLIsAbsolute(t *testing.T) {
	c := PerUserConnection{
		Name:       "atlassian",
		Kind:       "mcp",
		ConnectURL: "/account/mcp/connect?server=atlassian",
	}
	if !strings.HasPrefix(c.ConnectURL, "/") {
		t.Fatal("a relative connect URL resolves against whatever page shows it")
	}
	// And the value the manager actually builds. Read from the source
	// rather than reconstructed, so the test fails if the literal moves
	// back to a relative form.
	raw, err := os.ReadFile("mcp_manager.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if strings.Contains(src, `ConnectURL:  "mcp/connect?server=`) {
		t.Error("the manager builds an account-relative URL again")
	}
	if !strings.Contains(src, `ConnectURL:  "/account/mcp/connect?server=`) {
		t.Error("the manager should build the absolute consent path")
	}
	// The chat's Connect prompt has always used the absolute form; the
	// point of this change is that there is now ONE spelling of it.
	if strings.Count(src, "/account/mcp/connect") != 1 {
		t.Error("expected exactly one place in the manager to spell it")
	}
}
