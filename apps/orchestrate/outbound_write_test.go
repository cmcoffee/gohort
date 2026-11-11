package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

const heldWrite = "https://upload.example/bucket/file"

func heldWriteFor(canAsk bool) OutboundWrite {
	return OutboundWrite{Method: "PUT", URL: heldWrite, Host: "upload.example", CanAsk: canAsk}
}

func hostTrusted(owner, host string) bool {
	for _, e := range ListWriteHostPolicies(RootDB, owner) {
		if e.Target == host {
			return e.Policy == PolicyAllow
		}
	}
	return false
}

// Watched: the owner gets a card offering Always allow, and that answer
// trusts the host for every agent they own, so the next write does not ask.
// Somebody running the owner's shared agent can allow one call, not trust the
// host on the owner's behalf. A script is refused without a card.
func TestAWatchedWriteAsksTheOwner(t *testing.T) {
	pinRootDB(t)
	var offered [][]string
	answer := "Always allow upload.example"
	ask := func(prompt, detail string, yes []string) string {
		offered = append(offered, yes)
		for _, y := range yes {
			if y == answer {
				return y
			}
		}
		return ""
	}
	visitor := &chatTurn{user: "visitor", agent: AgentRecord{ID: "a1", Owner: "alice", Name: "Shared"}, askVia: ask}
	if r := visitor.approveWrite(heldWriteFor(true)); !strings.Contains(r, "declined") {
		t.Fatalf("a visitor's Always allow went through: %q", r)
	}
	if len(offered) != 1 || len(offered[0]) != 1 || offered[0][0] != "Allow once" {
		t.Fatalf("a visitor was offered %v", offered)
	}
	if r := visitor.approveWrite(heldWriteFor(false)); !strings.Contains(r, "fetch_url tool") || len(offered) != 1 {
		t.Fatalf("a script: %q (asked %d times)", r, len(offered))
	}
	owner := &chatTurn{user: "alice", agent: AgentRecord{ID: "a1", Owner: "alice", Name: "Shared"}, askVia: ask}
	if r := owner.approveWrite(heldWriteFor(true)); r != "" || !hostTrusted("alice", "upload.example") {
		t.Fatalf("the owner's Always allow: %q, trusted %v", r, hostTrusted("alice", "upload.example"))
	}
	if r := visitor.approveWrite(heldWriteFor(false)); r != "" || len(offered) != 2 {
		t.Fatalf("a trusted host still held the agent's script: %q (asked %d times)", r, len(offered))
	}
	if len(ListAuthorizations(RootDB, "alice")) != 0 {
		t.Error("a watched write queued a request")
	}
}

// Unwatched: refused and queued once per agent and host, and approving the
// request trusts the host, so the next run sends.
func TestAnUnattendedWriteIsQueuedAndApprovingTrustsTheHost(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	fire := &chatTurn{user: "alice", agent: AgentRecord{ID: "a1", Owner: "alice", Name: "Nightly"}}
	for i := 0; i < 2; i++ {
		if r := fire.approveWrite(heldWriteFor(i == 0)); !strings.Contains(r, "approval queue") {
			t.Fatalf("an unattended write: %q", r)
		}
	}
	var held []Authorization
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Action == outboundWriteAction {
			held = append(held, a)
		}
	}
	if len(held) != 1 || held[0].Handle != "upload.example" || held[0].Agent != "a1" || !strings.Contains(held[0].Brief, "PUT "+heldWrite) {
		t.Fatalf("queued %+v", held)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/console/approvals/approve?id="+held[0].ID, nil)
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, "alice"))
	if w.Code >= 300 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if !hostTrusted("alice", "upload.example") {
		t.Fatal("approving did not trust the host")
	}
	if r := fire.approveWrite(heldWriteFor(false)); r != "" {
		t.Fatalf("the next run was still held: %q", r)
	}
}

// The trusted hosts are on the Permissions page, under Workspace, binding
// every agent; Blocked holds and Remove puts the host back to asking.
func TestTrustedHostsShowOnThePermissionsPage(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	SetWriteHostPolicy(RootDB, "alice", "upload.example", PolicyAllow)
	row := rowByDetail(permRowsFor(t, app, "alice"), "Sending changes")
	if row == nil || row["Who"] != "upload.example" || row["_policy"] != PolicyAllow || row["_kind"] != "workspace" {
		t.Fatalf("row = %+v", row)
	}
	post := func(path string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		if w.Code >= 300 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/api/console/permissions/policy?id=writehost:upload.example&value=block")
	fire := &chatTurn{user: "alice", agent: AgentRecord{ID: "a1", Owner: "alice"}}
	if r := fire.approveWrite(heldWriteFor(true)); !strings.Contains(r, "blocked") {
		t.Fatalf("a blocked host: %q", r)
	}
	post("/api/console/permissions/remove?id=writehost:upload.example")
	if row := rowByDetail(permRowsFor(t, app, "alice"), "Sending changes"); row != nil {
		t.Fatalf("a removed host still shows: %+v", row)
	}
}

// A site is added ahead of time from the Security page's Network tab. A full
// link keeps only the site; something that cannot be one public site is
// refused with why. The tab's table lists the owner's sites and nothing else.
func TestASiteIsAddedBeforeAnAgentSendsToIt(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Nightly", OrchestratorPrompt: "p", WorkspaceNoNetwork: true}); err != nil {
		t.Fatal(err)
	}
	grant := func(subject, value string) int {
		t.Helper()
		body := `{"subject":` + strconvQuoteJSON(subject) + `,"value":"` + value + `"}`
		r := httptest.NewRequest(http.MethodPost, "/api/console/permissions/grant?kind=writehost&agent=a1", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routed(app)(w, asUser(r, "alice"))
		return w.Code
	}
	if code := grant("https://Hooks.Example.com:8443/services/x?y=1", "allow"); code >= 300 {
		t.Fatalf("adding a site: %d", code)
	}
	if !hostTrusted("alice", "hooks.example.com") {
		t.Fatal("the site part of the link was not trusted")
	}
	for _, bad := range []string{"", "localhost", "10.0.0.5", "*.example.com", "intranet"} {
		if code := grant(bad, "allow"); code != http.StatusBadRequest {
			t.Errorf("%q was taken as a site: %d", bad, code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/console/permissions?agent=a1&kind=workspace&scope=fleet", nil)
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, "alice"))
	if body := w.Body.String(); !strings.Contains(body, `"Who":"hooks.example.com"`) || strings.Contains(body, "Workspace may") {
		t.Fatalf("the Network tab's table: %s", body)
	}
}

func strconvQuoteJSON(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// A proxy tunnel to a site the owner has not trusted is refused without a
// card on a watched run, and on an unattended one queued like a write, worded
// as a connection, and approving it trusts the site for the next run.
func TestAnUntrustedTunnelIsRefusedAndQueuedUnattended(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	tunnel := OutboundWrite{Method: "CONNECT", URL: "upload.example:443", Host: "upload.example", Tunnel: true}
	watched := &chatTurn{user: "alice", agent: AgentRecord{ID: "a1", Owner: "alice"}, askVia: func(string, string, []string) string {
		t.Fatal("a tunnel showed a card")
		return ""
	}}
	if r := watched.approveWrite(tunnel); !strings.HasPrefix(r, "NOT CONNECTED") || len(ListAuthorizations(RootDB, "alice")) != 0 {
		t.Fatalf("watched: %q", r)
	}
	reg := tunnel
	reg.Host, reg.URL = "pypi.org", "pypi.org:443"
	if r := watched.approveWrite(reg); r != "" {
		t.Fatalf("a registry: %q", r)
	}
	fire := &chatTurn{user: "alice", agent: AgentRecord{ID: "a1", Owner: "alice", Name: "Nightly"}}
	if r := fire.approveWrite(tunnel); !strings.HasPrefix(r, "NOT CONNECTED") || !strings.Contains(r, "approval queue") {
		t.Fatalf("unattended: %q", r)
	}
	held := ListAuthorizations(RootDB, "alice")
	if len(held) != 1 || held[0].Action != outboundWriteAction || held[0].Handle != "upload.example" {
		t.Fatalf("queued %+v", held)
	}
	if _, detail := outboundWriteDisplay(nil, held[0]); !strings.Contains(detail, "connect to upload.example through a script's proxy") {
		t.Errorf("the request reads %q", detail)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/console/approvals/approve?id="+held[0].ID, nil)
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, "alice"))
	if w.Code >= 300 || !hostTrusted("alice", "upload.example") {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if r := fire.approveWrite(tunnel); r != "" {
		t.Fatalf("the next run was still refused: %q", r)
	}
}
