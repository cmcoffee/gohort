package core

// Contact permissions are scoped to the agent that earned them. See
// contactScopeKey for why, and docs/design/task-notes.md's sibling reasoning about a
// restriction that has to travel with the thing it restricts.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A permission and a guardrail have to share a scope, or the tighter one is
// decorative: an agent carries a persona and its own rules about what it may
// say to a person, and if the permission to reach that person belongs to the
// OWNER, handing the message to a different agent walks around all of it. That
// is not a bypass anybody has to intend; it is an ordinary dispatch.
func TestAContactGrantBelongsToTheAgentThatEarnedIt(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "careful-agent", "dana", PolicyAllow)

	if !IsContactPreAuthorized(db, "alice", "careful-agent", "dana") {
		t.Error("the agent that was granted cannot message the contact")
	}
	if IsContactPreAuthorized(db, "alice", "blunt-agent", "dana") {
		t.Error("another agent spent a grant it was never given")
	}
}

// Specific beats all-agents in BOTH directions, which is what makes the strict
// case expressible: an owner can promote a contact and still hold one agent
// back from them.
func TestAnAgentsOwnDecisionWinsOverThePromotedOne(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "", "dana", PolicyAllow) // every agent
	SetContactPolicy(db, "alice", "blunt-agent", "dana", PolicyBlock)

	if got := ContactPolicy(db, "alice", "careful-agent", "dana"); got != PolicyAllow {
		t.Errorf("an agent with no decision of its own should read the promoted one, got %q", got)
	}
	if got := ContactPolicy(db, "alice", "blunt-agent", "dana"); got != PolicyBlock {
		t.Errorf("a promoted grant overrode an agent the owner had already held back: %q", got)
	}
	// And the reverse: one agent allowed where nobody else is.
	SetContactPolicy(db, "alice", "", "erin", PolicyBlock)
	SetContactPolicy(db, "alice", "careful-agent", "erin", PolicyAllow)
	if got := ContactPolicy(db, "alice", "careful-agent", "erin"); got != PolicyAllow {
		t.Errorf("an agent's own exception was overridden by the general rule: %q", got)
	}
	if got := ContactPolicy(db, "alice", "blunt-agent", "erin"); got != PolicyBlock {
		t.Errorf("the general rule stopped applying to everybody else: %q", got)
	}
}

// Every grant made before contacts were scoped means "any agent of mine may
// message this person", because that is what it meant when it was made. Reading
// the old key shape as the all-agents row is what makes this change need no
// migration and break nothing an owner has working.
func TestGrantsMadeBeforeScopingStillWork(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	// Exactly what the old code wrote: the legacy preauth flag, unscoped.
	db.Set("contact_preauth", "alice:dana", true)

	for _, agent := range []string{"one", "two", ""} {
		if !IsContactPreAuthorized(db, "alice", agent, "dana") {
			t.Errorf("agent %q lost a grant that already existed", agent)
		}
	}
	// And an agent can still be held back from it afterwards.
	SetContactPolicy(db, "alice", "two", "dana", PolicyBlock)
	if IsContactPreAuthorized(db, "alice", "two", "dana") {
		t.Error("a legacy grant could not be narrowed")
	}
	if !IsContactPreAuthorized(db, "alice", "one", "dana") {
		t.Error("narrowing one agent changed another")
	}
}

// The listing has to say which scope a row is for, or the settings page cannot
// tell "this agent" from "every agent" and the promote button acts on a row the
// owner did not mean.
func TestListingCarriesTheScope(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "", "dana", PolicyAllow)
	SetContactPolicy(db, "alice", "careful-agent", "dana", PolicyBlock)

	var all, scoped int
	for _, e := range ListContactPolicies(db, "alice") {
		if e.Target != "dana" {
			t.Errorf("a scoped key leaked its agent into the target: %+v", e)
		}
		switch e.Scope {
		case "":
			all++
		case "careful-agent":
			scoped++
		default:
			t.Errorf("unexpected scope %q", e.Scope)
		}
	}
	if all != 1 || scoped != 1 {
		t.Errorf("expected one row per scope, got all=%d scoped=%d", all, scoped)
	}
}

// Delegation is the same question one layer in: which agent may hand work to
// which. A grant the whole fleet shares is one any agent can spend, and the
// careful routing an owner set up holds nothing.
func TestDelegationGrantsAreScopedToTheAgentDelegating(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetDelegationPolicy(db, "alice", "conductor", "invoices", PolicyAllow)

	if p, _ := DelegationDecision(db, "alice", "conductor", "invoices"); p != PolicyAllow {
		t.Error("the agent that was granted cannot delegate")
	}
	if p, _ := DelegationDecision(db, "alice", "stranger", "invoices"); p == PolicyAllow {
		t.Error("another agent spent a delegation grant it was never given")
	}
}

// The behaviour every dispatch surface already had, and the one most worth not
// breaking: an owner-wide block is about the TARGET, not the route taken to
// reach it. It refuses whoever asks.
func TestAnUnscopedDelegationBlockStillStopsEveryone(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetDelegationPolicy(db, "alice", "", "comedian", PolicyBlock)

	for _, from := range []string{"conductor", "stranger", ""} {
		if !IsDelegationBlocked(db, "alice", from, "comedian") {
			t.Errorf("agent %q could still delegate to a blocked target", from)
		}
	}
	// And one agent can be carved out of it, which is the point of scoping.
	SetDelegationPolicy(db, "alice", "conductor", "comedian", PolicyAllow)
	if IsDelegationBlocked(db, "alice", "conductor", "comedian") {
		t.Error("an agent's own exception did not survive the general block")
	}
	if !IsDelegationBlocked(db, "alice", "stranger", "comedian") {
		t.Error("carving out one agent lifted the block for everybody")
	}
}

// --- Outbound writes ---

func outboundTestDB(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
}

// The decision is keyed the way a host is written in a URL: case, a port and
// a trailing dot do not make a different host, and a subdomain does.
func TestAWriteHostDecisionIsKeyedByHost(t *testing.T) {
	outboundTestDB(t)
	if got := writeHostOf("https://Hooks.Example.COM.:8443/x?y=1"); got != "hooks.example.com" {
		t.Fatalf("host = %q", got)
	}
	if writeHostPolicy(RootDB, "alice", "hooks.example.com") != PolicyAsk {
		t.Fatal("an undecided host is not ask")
	}
	SetWriteHostPolicy(RootDB, "alice", "Hooks.Example.com", PolicyAllow)
	if writeHostPolicy(RootDB, "alice", "hooks.example.com") != PolicyAllow {
		t.Error("the decision does not read back under the normalised host")
	}
	if writeHostPolicy(RootDB, "alice", "other.hooks.example.com") != PolicyAsk {
		t.Error("trusting a host trusted its subdomains")
	}
	if writeHostPolicy(RootDB, "bob", "hooks.example.com") != PolicyAsk {
		t.Error("one owner's trust reached another owner")
	}
	if l := ListWriteHostPolicies(RootDB, "alice"); len(l) != 1 || l[0].Target != "hooks.example.com" || l[0].Policy != PolicyAllow || l[0].Scope != "" {
		t.Errorf("listing = %+v", l)
	}
	SetWriteHostPolicy(RootDB, "alice", "hooks.example.com", "")
	if writeHostPolicy(RootDB, "alice", "hooks.example.com") != PolicyAsk || len(ListWriteHostPolicies(RootDB, "alice")) != 0 {
		t.Error("removing the decision did not put the host back to ask")
	}
}

// With no app deciding, a write to an untrusted host asks the person in the
// chat, and Always allow trusts it from then on. A read never asks. A blocked
// host, a script and a run with nobody to ask are refused without a card.
func TestAWriteToAnUntrustedHostAsks(t *testing.T) {
	outboundTestDB(t)
	var asked []string
	answer := ""
	sess := &ToolSession{Username: "alice", AskInChat: func(prompt, detail string, yes []string) string {
		asked = append(asked, prompt)
		for _, y := range yes {
			if y == answer {
				return y
			}
		}
		return ""
	}}
	const u = "https://upload.example/bucket/file"
	if r := sess.OutboundWriteRefusal("GET", u, true); r != "" || len(asked) != 0 {
		t.Fatalf("a read was held: %q, asked %v", r, asked)
	}
	if r := sess.OutboundWriteRefusal("PUT", u, true); !strings.Contains(r, "declined") || len(asked) != 1 || !strings.HasSuffix(asked[0], "PUT request to this site: upload.example") {
		t.Fatalf("Deny: %q, asked %v", r, asked)
	}
	answer = "Allow once"
	if r := sess.OutboundWriteRefusal("post", u, true); r != "" || writeHostPolicy(RootDB, "alice", "upload.example") != PolicyAsk {
		t.Fatalf("Allow once: %q, and it trusted the host", r)
	}
	if r := sess.OutboundWriteRefusal("PUT", u, false); !strings.Contains(r, "fetch_url tool") || len(asked) != 2 {
		t.Fatalf("a script was asked or not told what to do: %q, asked %d", r, len(asked))
	}
	answer = "Always allow upload.example"
	if r := sess.OutboundWriteRefusal("PATCH", u, true); r != "" || writeHostPolicy(RootDB, "alice", "upload.example") != PolicyAllow {
		t.Fatalf("Always allow: %q", r)
	}
	if r := sess.OutboundWriteRefusal("DELETE", u, false); r != "" || len(asked) != 3 {
		t.Fatalf("a trusted host still held a script or asked again: %q, asked %d", r, len(asked))
	}
	SetWriteHostPolicy(RootDB, "alice", "upload.example", PolicyBlock)
	if r := sess.OutboundWriteRefusal("PUT", u, true); !strings.Contains(r, "blocked") || len(asked) != 3 {
		t.Fatalf("a blocked host: %q, asked %d", r, len(asked))
	}
	if r := (&ToolSession{Username: "alice"}).OutboundWriteRefusal("PUT", "https://other.example/", true); !strings.Contains(r, "nobody is watching") {
		t.Fatalf("nobody to ask: %q", r)
	}
	if r := (*ToolSession)(nil).OutboundWriteRefusal("PUT", "https://other.example/", false); !strings.HasPrefix(r, "NOT SENT") {
		t.Fatalf("no session: %q", r)
	}
}

// An app that decides for itself decides everything: its answer is the one
// returned, with the method and host already worked out.
func TestTheAppDecidesAWriteWhenItSetsApproveWrite(t *testing.T) {
	outboundTestDB(t)
	SetWriteHostPolicy(RootDB, "visitor", "upload.example", PolicyAllow)
	var got []string
	sess := &ToolSession{Username: "visitor", ApproveWrite: func(w OutboundWrite) string {
		got = append(got, w.Method, w.Host)
		return "NOT SENT: app"
	}, AskInChat: func(string, string, []string) string { t.Fatal("the plain card was used"); return "" }}
	if r := sess.OutboundWriteRefusal("post", "https://upload.example/x", true); r != "NOT SENT: app" || strings.Join(got, " ") != "POST upload.example" {
		t.Fatalf("refusal %q, app saw %v", r, got)
	}
}

// A script's PUT through the fetch hook to a host nobody trusted is refused
// before anything is dialled, and says to use the fetch_url tool.
func TestTheFetchHookRefusesAnUntrustedWrite(t *testing.T) {
	outboundTestDB(t)
	h := &SandboxHook{Capabilities: []string{"fetch"}, Sess: &ToolSession{Username: "alice"}}
	a, b := net.Pipe()
	go func() {
		h.handleFetch(a, map[string]interface{}{"url": "https://upload.example/bucket/file", "method": "PUT", "body": "payload"})
		a.Close()
	}()
	out, _ := io.ReadAll(b)
	if !strings.Contains(string(out), "fetch refused: NOT SENT") || !strings.Contains(string(out), "fetch_url tool") {
		t.Fatalf("hook answer: %s", out)
	}
}

// A proxy tunnel shows gohort its host and never its method, so it is decided
// by host: a package registry passes, a site the owner trusts passes, and any
// other site is refused without a card (a proxy client cannot wait for one)
// and told how to read or send instead. The owner blocking a registry wins,
// and a registry's name covers that exact host, not others under its domain.
func TestAProxyTunnelGoesToRegistriesAndTrustedSitesOnly(t *testing.T) {
	outboundTestDB(t)
	sess := &ToolSession{Username: "alice", AskInChat: func(string, string, []string) string {
		t.Fatal("a tunnel showed a card")
		return ""
	}}
	if r := sess.OutboundTunnelRefusal("pypi.org"); r != "" {
		t.Fatalf("a package registry: %q", r)
	}
	for _, host := range []string{"upload.example", "files.pythonhosted.org.evil.example", "bucket.storage.example", "203.0.113.9"} {
		if r := sess.OutboundTunnelRefusal(host); !strings.HasPrefix(r, "NOT CONNECTED") || !strings.Contains(r, "curl") || !strings.Contains(r, "fetch_url") {
			t.Errorf("%s: %q", host, r)
		}
	}
	SetWriteHostPolicy(RootDB, "alice", "upload.example", PolicyAllow)
	if r := sess.OutboundTunnelRefusal("Upload.Example."); r != "" {
		t.Errorf("a trusted site: %q", r)
	}
	SetWriteHostPolicy(RootDB, "alice", "pypi.org", PolicyBlock)
	if r := sess.OutboundTunnelRefusal("pypi.org"); !strings.Contains(r, "blocked") {
		t.Errorf("a registry the owner blocked: %q", r)
	}
	var got OutboundWrite
	app := &ToolSession{Username: "alice", ApproveWrite: func(w OutboundWrite) string { got = w; return "NOT CONNECTED: app" }}
	if r := app.OutboundTunnelRefusal("Upload.Example."); r != "NOT CONNECTED: app" || !got.Tunnel || got.CanAsk || got.Method != "CONNECT" || got.Host != "upload.example" || got.URL != "upload.example:443" {
		t.Errorf("the app decides a tunnel: %q, saw %+v", r, got)
	}
}

// Through the relay itself: a CONNECT to a site nobody trusted gets a 403
// that says why, and nothing is dialled; one to a registry goes on to the
// dial.
func TestTheProxyRefusesATunnelToAnUntrustedSite(t *testing.T) {
	outboundTestDB(t)
	var dialled []string
	prev := relayDial
	t.Cleanup(func() { relayDial = prev })
	relayDial = func(ctx context.Context, addr string) (net.Conn, error) {
		dialled = append(dialled, addr)
		return nil, errors.New("test: no network")
	}
	h := &SandboxHook{Capabilities: []string{"proxy"}, RelayToken: "run-token", Sess: &ToolSession{Username: "alice"}}
	connect := func(host string) string {
		a, b := net.Pipe()
		go func() {
			h.handleRelay(a, bufio.NewReader(a))
			a.Close()
		}()
		auth := base64.StdEncoding.EncodeToString([]byte("gohort:run-token"))
		go b.Write([]byte("CONNECT " + host + ":443 HTTP/1.1\r\nHost: " + host + ":443\r\nProxy-Authorization: Basic " + auth + "\r\n\r\n"))
		out, _ := io.ReadAll(b)
		return string(out)
	}
	if out := connect("upload.example"); !strings.HasPrefix(out, "HTTP/1.1 403") || !strings.Contains(out, "NOT CONNECTED") || len(dialled) != 0 {
		t.Fatalf("an untrusted site: dialled %v\n%s", dialled, out)
	}
	if out := connect("registry.npmjs.org"); !strings.HasPrefix(out, "HTTP/1.1 502") || len(dialled) != 1 || dialled[0] != "registry.npmjs.org:443" {
		t.Fatalf("a registry was not dialled: %v\n%s", dialled, out)
	}
}

// The administrator's list sits around each owner's: a Never there refuses
// for everyone, over an owner's Always allow and a package registry; an
// Always allow there trusts a site nobody decided about, and gives way to an
// owner's own Never. Only allow and block are kept; anything else removes.
func TestTheAdministratorsSiteListComesFirst(t *testing.T) {
	outboundTestDB(t)
	never := func(string, string, []string) string { t.Fatal("a decided site showed a card"); return "" }
	alice := &ToolSession{Username: "alice", AskInChat: never}
	const u = "https://upload.example/bucket/file"

	SetWriteHostPolicy(RootDB, "alice", "upload.example", PolicyAllow)
	AdminSites.Set(RootDB, "Upload.Example.", PolicyBlock)
	if r := alice.OutboundWriteRefusal("PUT", u, true); !strings.Contains(r, "administrator has blocked") {
		t.Errorf("the administrator's Never under an owner's Always allow: %q", r)
	}
	if r := alice.OutboundTunnelRefusal("upload.example"); !strings.HasPrefix(r, "NOT CONNECTED") || !strings.Contains(r, "administrator") {
		t.Errorf("a tunnel to a site the administrator blocked: %q", r)
	}
	AdminSites.Set(RootDB, "pypi.org", PolicyBlock)
	if r := alice.OutboundTunnelRefusal("pypi.org"); !strings.Contains(r, "administrator") {
		t.Errorf("a registry the administrator blocked: %q", r)
	}

	AdminSites.Set(RootDB, "hooks.example", PolicyAllow)
	bob := &ToolSession{Username: "bob", AskInChat: never}
	if r := bob.OutboundWriteRefusal("POST", "https://hooks.example/x", false); r != "" {
		t.Errorf("a site the administrator trusts, from a script: %q", r)
	}
	if r := bob.OutboundTunnelRefusal("hooks.example"); r != "" {
		t.Errorf("a tunnel to a site the administrator trusts: %q", r)
	}
	SetWriteHostPolicy(RootDB, "bob", "hooks.example", PolicyBlock)
	if r := bob.OutboundWriteRefusal("POST", "https://hooks.example/x", true); !strings.Contains(r, "owner has blocked") {
		t.Errorf("an owner's own Never under the administrator's Always allow: %q", r)
	}

	if l := AdminSites.List(RootDB); len(l) != 3 || l[0].Target != "hooks.example" || l[1].Target != "pypi.org" || l[2].Target != "upload.example" || l[2].Policy != PolicyBlock {
		t.Fatalf("listing = %+v", l)
	}
	AdminSites.Set(RootDB, "upload.example", PolicyAsk)
	if AdminSites.Policy(RootDB, "upload.example") != "" {
		t.Error("Ask first was kept at the administrator's level, where it means no decision")
	}
	if r := alice.OutboundWriteRefusal("PUT", u, true); r != "" {
		t.Errorf("with the administrator's Never gone, the owner's Always allow applies again: %q", r)
	}
}

// A typed address becomes the site the check keys on, or a reason it cannot.
func TestSiteFromInput(t *testing.T) {
	for in, want := range map[string]string{
		"https://Hooks.Example.com:8443/path?q=1": "hooks.example.com",
		"hooks.example.com.":                      "hooks.example.com",
		"203.0.113.9":                             "203.0.113.9",
	} {
		if got, why := SiteFromInput(in); got != want || why != "" {
			t.Errorf("%q -> %q (%s), want %q", in, got, why, want)
		}
	}
	for _, in := range []string{"", "*.example.com", "intranet", "192.168.1.5", "localhost"} {
		if got, why := SiteFromInput(in); why == "" {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

// An app check's run writes nothing outward: a write is refused even where
// the app's own decision would let it go, a CONNECT tunnel is refused, and a
// read goes out as usual.
func TestAnAppCheckRunWritesNothingOutward(t *testing.T) {
	trusting := &ToolSession{Username: "owner-a", CheckRun: true, ApproveWrite: func(OutboundWrite) string { return "" }}
	if r := trusting.OutboundWriteRefusal("POST", "https://api.example.com/send", true); r == "" {
		t.Error("a check run's write went out because the host was trusted")
	}
	if r := trusting.OutboundTunnelRefusal("api.example.com"); r == "" {
		t.Error("a check run opened a tunnel")
	}
	if r := trusting.CheckRunRefusal("PUT"); r == "" {
		t.Error("a credential-routed write from a check run was not refused")
	}
	if r := trusting.OutboundWriteRefusal("GET", "https://api.example.com/read", true); r != "" {
		t.Errorf("a check run's read was refused: %s", r)
	}
	if r := (&ToolSession{Username: "owner-a"}).CheckRunRefusal("POST"); r != "" {
		t.Errorf("an ordinary run was held to the check rule: %s", r)
	}
}
