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
