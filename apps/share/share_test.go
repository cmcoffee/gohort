package share

// The app's own job is small and specific: split a grant into one row per
// person, never let a request act on somebody else's records, and name no kind.

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/shareledger"
)

// A grant naming three people is three decisions. One row with one Take back
// over all of them takes back more than the button says.
func TestAGrantToSeveralPeopleIsSeveralRows(t *testing.T) {
	g := shareledger.Grant{Kind: "skill", Label: "Skill", ID: "s1", Name: "Triage",
		Recipients: []string{"bob", "carol"}, Reach: "Shared with bob, carol", Revocable: true}

	var rows []row
	for _, u := range g.Recipients {
		rows = append(rows, toRow(g, g.Reach, u))
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Who != "bob" || rows[1].Who != "carol" {
		t.Errorf("the rows do not name one person each: %+v", rows)
	}
	// Each carries the one name its button acts on, so a revoke cannot reach
	// past the row somebody clicked.
	if rows[0].Recipient != "bob" || rows[1].Recipient != "carol" {
		t.Errorf("a row's revoke is not scoped to its own person: %+v", rows)
	}
	if rows[0].ID == rows[1].ID {
		t.Error("two rows share a key, so the table will treat them as one")
	}
}

// A deployment-wide grant has no recipient to name, and saying "everybody" is
// the honest answer rather than a blank cell.
func TestAWideGrantSaysEverybody(t *testing.T) {
	r := toRow(shareledger.Grant{Kind: "skill", Label: "Skill", ID: "s1", Wide: true}, "Deployment-wide", "")
	if r.Who != "Everybody" {
		t.Errorf("who = %q", r.Who)
	}
	if r.Recipient != "" {
		t.Errorf("a wide grant named a single recipient: %q", r.Recipient)
	}
}

// The owner is the SESSION user, never a parameter. A revoke that took an
// owner from the query string would let anybody take back anybody's shares.
func TestRevokeNeverTakesAnOwnerFromTheRequest(t *testing.T) {
	src, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	i := strings.Index(string(src), "func (T *ShareApp) serveRevoke")
	if i < 0 {
		t.Fatal("serveRevoke is gone")
	}
	body := string(src)[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, `Query().Get("owner")`) {
		t.Error("the revoke takes an owner from the request; it must be the session user")
	}
	if !strings.Contains(body, "shareledger.Revoke(kind, user, id, recipient)") {
		t.Error("the revoke no longer passes the session user as the owner")
	}
}

// This app renders whatever registered. The moment it names a kind, a kind
// added later stops appearing without an edit here — which is the whole thing
// the registry exists to prevent.
func TestTheAppNamesNoKind(t *testing.T) {
	src, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	// In string literals, which is where a leak would show: a column label, a
	// heading, a branch on one kind's name.
	for _, kind := range []string{
		`"agent"`, `"skill"`, `"collection"`, `"credential"`, `"pipeline"`, `"machine"`,
	} {
		if strings.Contains(string(src), kind) {
			t.Errorf("the share app names the kind %s; it should only render what registered", kind)
		}
	}
}

// The guided route's own job: carry a decision key through a form field name
// and back, and refuse to act on anything the first screen did not name.
func TestADecisionKeySurvivesTheRoundTrip(t *testing.T) {
	for _, k := range []string{"cred:wiki", "skill:s1", "tool:ssh_run", "collection:col-1"} {
		if got := unsafeKey(safeKey(k)); got != k {
			t.Errorf("%q came back as %q", k, got)
		}
		if strings.Contains(safeKey(k), ":") {
			t.Errorf("%q still carries a colon as a field name: %q", k, safeKey(k))
		}
	}
}

// Every screen after the first says what is being shared. Quoting an id back
// at somebody mid-flow is how they lose track of which thing they picked.
func TestTheDecisionsScreenNamesTheThing(t *testing.T) {
	src, err := os.ReadFile("guided.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	if !strings.Contains(string(src), "candidateName(user, kind, id)") {
		t.Error("the plan page no longer resolves a display name")
	}
}

// Nothing happens until the second screen is confirmed. A first step that
// shared anything would make "Continue" a commitment nobody agreed to.
func TestTheFirstStepSharesNothing(t *testing.T) {
	src, err := os.ReadFile("guided.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	i := strings.Index(string(src), "func (T *ShareApp) servePlan(")
	if i < 0 {
		t.Fatal("servePlan is gone")
	}
	body := string(src)[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, "shareledger.Share(") {
		t.Error("the first step shares before anybody has seen what it decides")
	}
}

// The guided flow renders whatever the kind asked and knows none of it. The
// moment it branches on a decision key, the kind that owns the meaning has
// lost it.
func TestTheGuidedFlowNamesNoKind(t *testing.T) {
	src, err := os.ReadFile("guided.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	for _, kind := range []string{
		`"agent"`, `"skill"`, `"collection"`, `"pipeline"`, `"machine"`,
		// "credential" is the one a reader would most expect to find here,
		// since its fork is the reason the flow exists. It is not: what that
		// decision means lives with credentials, and only what it LOOKS like
		// lives here.
		`"credential"`, `"cred:"`,
	} {
		if strings.Contains(string(src), kind) {
			t.Errorf("the guided flow names %s; it should render only what a kind returned", kind)
		}
	}
}

// Asking what somebody else's agent carries is not a question this answers for
// anybody who did not receive it. The gate is the ledger itself: if it is not
// in your "shared with you", there is nothing here to read.
func TestOnlyARecipientCanSeeWhatSomethingCarries(t *testing.T) {
	src, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	i := strings.Index(string(src), "func (T *ShareApp) serveCarries")
	if i < 0 {
		t.Fatal("serveCarries is gone")
	}
	body := string(src)[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "shareledger.ToMe(user)") {
		t.Error("the carries listing does not check that the caller actually holds it")
	}
	if !strings.Contains(body, "http.NotFound") {
		t.Error("a caller who does not hold it is not refused")
	}
	// The viewer is the session user, never a parameter — the same rule the
	// revoke follows, and for the same reason.
	if strings.Contains(body, `Query().Get("user")`) || strings.Contains(body, `Query().Get("viewer")`) {
		t.Error("the carries listing takes a viewer from the request")
	}
}

// A redirect URL is a TEMPLATE, never a whole URL handed over in one
// placeholder.
//
// The runtime's substituter URL-encodes whatever it places, which is right —
// an id with a slash in it would otherwise break the path. Give it a finished
// URL and the query string is encoded along with everything else, so
// "plan?kind=agent&id=a1" becomes one path segment and lands on a 404. That
// shipped, and this is what it cost to find.
func TestTheRedirectIsATemplateNotAWholeURL(t *testing.T) {
	src, err := os.ReadFile("guided.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	s := string(src)
	i := strings.Index(s, "RedirectURL:")
	if i < 0 {
		t.Fatal("the redirect is gone")
	}
	line := s[i:]
	if end := strings.Index(line, "\n"); end > 0 {
		line = line[:end]
	}
	// A single placeholder for the whole value is the shape that fails.
	if strings.Contains(line, `"{`) && strings.Count(line, "{") == 1 {
		t.Errorf("the redirect hands the substituter a whole URL, which it will encode entire:\n  %s", strings.TrimSpace(line))
	}
	// The query structure has to be literal, so only the VALUES get encoded.
	for _, want := range []string{"plan?", "kind={kind}", "id={id}", "who={who}"} {
		if !strings.Contains(line, want) {
			t.Errorf("the redirect template is missing %q:\n  %s", want, strings.TrimSpace(line))
		}
	}
	// And the endpoint that feeds it returns the parts, not a finished URL.
	if strings.Contains(s, `"url": "plan?`) {
		t.Error("servePlan still builds a whole URL for the redirect to encode")
	}
}
