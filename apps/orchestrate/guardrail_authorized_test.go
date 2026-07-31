package orchestrate

// The "@" marker exempts a rule for someone the FRAMEWORK established as
// authorized. Everything worth pinning here is about where that fact comes
// from: it must be resolvable from the dispatch path and the transport's
// attribution, and unreachable from anything a requester writes.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func webTurn(t *testing.T, agent AgentRecord, actingUser string) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	return &chatTurn{
		app: app, agent: agent, ctx: context.Background(),
		user: actingUser, udb: UserDB(root, actingUser),
		ownerUser: agent.Owner, ownerDB: UserDB(root, agent.Owner),
	}
}

// TestMarkersStack — severity and the exemption answer different questions, so
// a rule can carry both. The parser used to switch on ONE leading prefix, which
// made them exclusive by accident rather than by design.
func TestMarkersStack(t *testing.T) {
	cases := []struct {
		line       string
		text       string
		correct    bool
		contest    bool
		exceptAuth bool
	}{
		{"never discuss pay", "never discuss pay", false, false, false},
		{"? keep it under 200 words", "keep it under 200 words", true, false, false},
		{"~ no jokes unless asked twice", "no jokes unless asked twice", false, true, false},
		{"@ never discuss pay", "never discuss pay", false, false, true},
		{"@? keep it under 200 words", "keep it under 200 words", true, false, true},
		{"?@ keep it under 200 words", "keep it under 200 words", true, false, true},
		{"@ ~ no jokes unless asked twice", "no jokes unless asked twice", false, true, true},
		{"! never discuss pay", "never discuss pay", false, false, false}, // legacy, stripped
		{"@! never discuss pay", "never discuss pay", false, false, true},
	}
	for _, c := range cases {
		got := parseGuardrailRule(c.line)
		if got.Text != c.text {
			t.Errorf("%q → text %q, want %q", c.line, got.Text, c.text)
		}
		if got.Correctable != c.correct || got.Contestable != c.contest || got.ExceptAuthorized != c.exceptAuth {
			t.Errorf("%q → correctable=%v contestable=%v exceptAuth=%v, want %v/%v/%v",
				c.line, got.Correctable, got.Contestable, got.ExceptAuthorized, c.correct, c.contest, c.exceptAuth)
		}
	}
}

// TestMarkerOnlyLineKeepsItsText — a line of nothing but markers has no rule in
// it. Turning it into an empty rule would match every candidate ever judged.
func TestMarkerOnlyLineKeepsItsText(t *testing.T) {
	for _, line := range []string{"@", "?", "~", "@?", "@ ~ "} {
		got := parseGuardrailRule(line)
		if got.Text != strings.TrimSpace(line) {
			t.Errorf("%q → text %q, want the line kept verbatim", line, got.Text)
		}
		if got.ExceptAuthorized || got.Correctable || got.Contestable {
			t.Errorf("%q set flags on a line with no rule in it", line)
		}
	}
}

// TestExemptRulesLeaveThePlayForAuthorized — the marker is resolved BEFORE the
// warden call, so an exempt rule is not judged at all.
func TestExemptRulesLeaveThePlayForAuthorized(t *testing.T) {
	rules := []guardrailRule{
		{Text: "never discuss pay", ExceptAuthorized: true},
		{Text: "never send money", ExceptAuthorized: false},
	}
	authorized := rulesInPlayFor(rules, requesterIdentity{Authorized: true})
	if len(authorized) != 1 || authorized[0].Text != "never send money" {
		t.Fatalf("authorized requester should face only the unmarked rule, got %v", authorized)
	}
	stranger := rulesInPlayFor(rules, requesterIdentity{})
	if len(stranger) != 2 {
		t.Errorf("an unauthorized requester faces every rule, got %d", len(stranger))
	}
	// Nothing marked: byte-identical behaviour to before the marker existed.
	plain := []guardrailRule{{Text: "a"}, {Text: "b"}}
	if got := rulesInPlayFor(plain, requesterIdentity{Authorized: true}); len(got) != 2 {
		t.Errorf("unmarked rules must survive an authorized requester, got %d", len(got))
	}
}

// TestOwnerIsAuthorizedWithoutBeingListed — they wrote the roster; requiring
// them to add themselves would be a trap that silently gags them.
func TestOwnerIsAuthorizedWithoutBeingListed(t *testing.T) {
	agent := AgentRecord{ID: "a1", Owner: "u", Guardrails: "@ never discuss pay"}
	who := webTurn(t, agent, "u").requester()
	if !who.Owner || !who.Authorized {
		t.Fatalf("owner should be authorized over their own agent, got %+v", who)
	}
	if who.AuthorizedVia != guardAuthAuthenticated {
		t.Errorf("owner authorization should be authenticated, got %q", who.AuthorizedVia)
	}
}

// TestRosterMatchesAuthenticatedAccount — the strong route.
func TestRosterMatchesAuthenticatedAccount(t *testing.T) {
	agent := AgentRecord{ID: "a1", Owner: "u", AuthorizedIdentities: []string{"dana"}}
	who := webTurn(t, agent, "dana").requester()
	if who.Owner {
		t.Fatal("a roster entry must not be promoted to owner")
	}
	if !who.Authorized || who.AuthorizedAs != "dana" || who.AuthorizedVia != guardAuthAuthenticated {
		t.Fatalf("expected dana authorized by account, got %+v", who)
	}
	// Someone not on it stays an outside party.
	other := webTurn(t, agent, "mallory").requester()
	if other.Authorized {
		t.Error("an account absent from the roster must not be authorized")
	}
}

// TestSelfReportedNameNeverAuthorizes — the whole safety property. A contact
// picks their own display name, so it is exactly the field an attacker sets to
// a roster entry.
func TestSelfReportedNameNeverAuthorizes(t *testing.T) {
	agent := AgentRecord{ID: "a1", Owner: "u", AuthorizedIdentities: []string{"dana", "dana@example.com"}}
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	turn := &chatTurn{
		app: app, agent: agent, ctx: context.Background(),
		user: "phantom:chat123", udb: UserDB(root, "phantom:chat123"),
		ownerUser: "u", ownerDB: UserDB(root, "u"),
		requesterName: "dana", // claimed, not established
	}
	who := turn.requester()
	if who.Authorized {
		t.Fatal("a self-reported name conferred authorization")
	}
	if strings.Contains(who.describe(), "AUTHORIZED") {
		t.Errorf("the trusted line claimed authorization for a stranger: %s", who.describe())
	}
	// And a synthetic channel identity must not match a roster entry by string
	// equality with the acting user either.
	turn.user = "phantom:dana"
	if turn.requester().Authorized {
		t.Error("a synthetic per-chat identity was treated as an account")
	}
}

// TestUnresolvableAuthorizationLeavesTheRuleInForce — the failure direction.
// No bridge, no handle, no match: the rule applies.
func TestUnresolvableAuthorizationLeavesTheRuleInForce(t *testing.T) {
	agent := AgentRecord{ID: "a1", Owner: "u", AuthorizedIdentities: []string{"+15550109999"}}
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	turn := &chatTurn{
		app: app, agent: agent, ctx: context.Background(),
		user: "phantom:chat123", udb: UserDB(root, "phantom:chat123"),
		ownerUser: "u", ownerDB: UserDB(root, "u"),
		requesterHandle: "+15550109999", // right handle, but no bridge is active
	}
	who := turn.requester()
	if who.Authorized {
		t.Fatal("authorization resolved with no messaging bridge to vouch for the handle")
	}
	rules := []guardrailRule{{Text: "never discuss pay", ExceptAuthorized: true}}
	if got := rulesInPlayFor(rules, who); len(got) != 1 {
		t.Error("an unresolvable authorization must leave the rule in force")
	}
}

// TestAuthorizedDescribeNamesTheRouteAndNotTheOwner — a rule may be written to
// care whether the authorization was a session or a phone, so the warden has to
// be told which, and must never read an authorized party as the owner.
func TestAuthorizedDescribeNamesTheRouteAndNotTheOwner(t *testing.T) {
	who := requesterIdentity{Authorized: true, AuthorizedAs: "dana", AuthorizedVia: guardAuthHandle, Channel: "imessage"}
	got := who.describe()
	for _, want := range []string{"NOT the owner", "AUTHORIZED", "dana", guardAuthHandle, "imessage"} {
		if !strings.Contains(got, want) {
			t.Errorf("trusted line missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "SELF-REPORTED") {
		t.Errorf("an established identity should not be described as self-reported: %s", got)
	}
}

// TestSanitizeAuthorizedIdentities — nothing the owner typed may be discarded
// on its SHAPE. The version that rejected any entry containing a space threw
// away the commonest entry there is (a phone number written with spaces, which
// SameHandle matches perfectly after the bridge normalizes both sides) and did
// it silently, which is what produced "authorized=0/1 kept" in the save log and
// a roster that looked like it would not save.
func TestSanitizeAuthorizedIdentities(t *testing.T) {
	got := sanitizeAuthorizedIdentities([]string{
		" dana ", "dana", "", "   ",
		"+1 555 010 9999",  // spaced phone: matches after normalization
		"dana@example.com", // email
		"Dana Whitfield",   // display name: inert, but kept and VISIBLE
	})
	want := []string{"dana", "+1 555 010 9999", "dana@example.com", "Dana Whitfield"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
	long := make([]string, maxAuthorizedIdentities+5)
	for i := range long {
		long[i] = "user" + strings.Repeat("x", i)
	}
	if n := len(sanitizeAuthorizedIdentities(long)); n != maxAuthorizedIdentities {
		t.Errorf("roster cap not applied: kept %d", n)
	}
}
