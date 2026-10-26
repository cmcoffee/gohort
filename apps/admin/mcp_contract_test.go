package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the MCP section gets back, through the real routes and the gate in
// front of them: the remote MCP servers listed, read, saved (their token never
// sent back), switched on and off, tested and deleted; and the app tools
// gohort's own /mcp/ endpoint may expose, listed and switched. Each as an
// administrator, as a user who is not one, by the wrong method and with a
// broken or missing body. A server here points at a closed local port, so
// nothing reaches past this machine. Compared with
// testdata/mcp_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestMCPRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	// The MCP manager is the process's own and outlives a test: start from
	// none of this test's servers.
	for _, c := range MCP().List() {
		if strings.HasPrefix(c.Name, "ct-") {
			MCP().Delete(c.Name)
		}
	}
	RegisterMCPTool(MCPToolSpec{Name: "ct_app_tool", Description: "A tool an app offers.",
		Handler: func(context.Context, string, map[string]any) (string, error) { return "ok", nil }})

	// Left out of what is recorded: servers and app tools other tests (and
	// apps) leave in the process's registries.
	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-mcp"},
		keep: func(row string) bool {
			return !strings.Contains(row, `"name":`) || strings.Contains(row, `"name":"ct-`) || strings.Contains(row, `"name":"ct_`)
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }
	// An empty server list is null, and one whose only rows are another
	// test's is [] once those are left out: the same answer here.
	ct.shape = func(out string) string {
		if out == "null" {
			return "[]"
		}
		return out
	}

	for _, p := range []string{"/api/mcp-servers", "/api/mcp-servers?name=ct-docs", "/api/mcp-tools"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	do("alice", "POST", "/api/mcp-servers/test", `{}`)
	do("root", "GET", "/api/mcp-servers/test", "")

	do("alice", "POST", "/api/mcp-servers", `{"name":"ct-mine","url":"http://127.0.0.1:1/mcp"}`)
	do("root", "POST", "/api/mcp-servers", `{"name":"ct-docs","url":"http://127.0.0.1:1/mcp","auth_mode":"bearer","token":"s3cr3t-mcp","expose_tools":true,"enabled":false}`)
	do("root", "POST", "/api/mcp-servers", `{"name":"Bad Name","url":"http://127.0.0.1:1/mcp"}`)
	do("root", "POST", "/api/mcp-servers", `{"name":"ct-ftp","url":"ftp://127.0.0.1/mcp"}`)
	do("root", "POST", "/api/mcp-servers", `{"name":"ct-vault","url":"http://127.0.0.1:1/mcp","auth_mode":"secure_api"}`)
	do("root", "POST", "/api/mcp-servers", `{"name":`)
	do("root", "POST", "/api/mcp-servers", ``)
	do("root", "GET", "/api/mcp-servers?name=ct-docs", "")
	do("root", "GET", "/api/mcp-servers?name=ct-nope", "")
	do("root", "GET", "/api/mcp-servers", "")

	do("alice", "POST", "/api/mcp-servers?action=enable&name=ct-docs", "")
	do("root", "POST", "/api/mcp-servers?action=enable", "")
	do("root", "POST", "/api/mcp-servers?action=enable&name=ct-nope", "")
	do("root", "POST", "/api/mcp-servers?action=sideways&name=ct-docs", "")
	do("root", "POST", "/api/mcp-servers?action=disable&name=ct-docs", "")
	do("root", "GET", "/api/mcp-servers?name=ct-docs", "")

	do("root", "POST", "/api/mcp-servers/test", `{"name":"ct-docs","url":"http://127.0.0.1:1/mcp","auth_mode":"bearer","token":"s3cr3t-mcp"}`)
	do("root", "POST", "/api/mcp-servers/test", `{"name":"ct-docs","url":"ftp://127.0.0.1/mcp"}`)
	do("root", "POST", "/api/mcp-servers/test", `{"name":`)
	do("root", "POST", "/api/mcp-servers/test", ``)

	do("alice", "POST", "/api/mcp-tools?name=ct_app_tool", `{"exposed":true}`)
	do("root", "POST", "/api/mcp-tools?name=ct_app_tool", `{"exposed":true}`)
	do("root", "GET", "/api/mcp-tools", "")
	do("root", "POST", "/api/mcp-tools?name=ct_app_tool", `{"exposed":false}`)
	do("root", "POST", "/api/mcp-tools", `{"exposed":true}`)
	do("root", "POST", "/api/mcp-tools?name=ct_nope", `{"exposed":true}`)
	do("root", "POST", "/api/mcp-tools?name=ct_app_tool", `{"exposed":`)
	do("root", "POST", "/api/mcp-tools?name=ct_app_tool", ``)
	do("root", "GET", "/api/mcp-tools", "")

	do("alice", "DELETE", "/api/mcp-servers?name=ct-docs", "")
	do("root", "DELETE", "/api/mcp-servers", "")
	do("root", "DELETE", "/api/mcp-servers?name=ct-nope", "")
	do("root", "DELETE", "/api/mcp-servers?name=ct-docs", "")
	do("root", "GET", "/api/mcp-servers", "")

	ct.check(t, "testdata/mcp_contract.golden")
}
