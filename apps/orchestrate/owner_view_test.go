package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Everything describing an AGENT — its record, its fleet peers, and the
// custom-tool pool its AllowedTools names — resolves against the owner's store.
// Sessions, memory and knowledge stay with whoever is typing. The two are the
// same identity on an ordinary turn and differ on exactly the runs that matter:
// a phantom/channel run under a synthetic per-chat user, and a PUBLISHED agent
// chatted by someone who is not its author.
//
// The web path used to pass the session's identity flat as the tool pool, so a
// published agent resolved its own AllowedTools against a visitor's store,
// found none of them, and carried on claiming the capabilities in its prompt.
func TestOwnerViewPrefersTheAgentOwner(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	visitorDB := UserDB(root, "visitor")
	ownerDB := UserDB(root, "author")

	// Ordinary turn: one identity, so the owner view IS the runtime view. This
	// is the case that must not change.
	own := &chatTurn{user: "author", udb: ownerDB}
	if db, u := own.ownerView(); u != "author" || db == nil {
		t.Fatalf("owner-driven turn resolved to %q, want the runtime identity", u)
	}

	// Published agent: the visitor types, the author owns the agent.
	pub := &chatTurn{user: "visitor", udb: visitorDB, ownerUser: "author", ownerDB: ownerDB}
	db, u := pub.ownerView()
	if u != "author" {
		t.Fatalf("published-agent turn resolved the pool to %q, want the author", u)
	}
	if db == nil {
		t.Fatal("published-agent turn resolved a nil owner store")
	}
	// And the runtime identity is untouched — this split is the whole point.
	if pub.user != "visitor" || pub.udb == nil {
		t.Fatal("the runtime identity must stay with the visitor")
	}

	// Half-set is not enough: both halves or fall back, or a caller that sets
	// only a username silently reads the wrong store.
	half := &chatTurn{user: "visitor", udb: visitorDB, ownerUser: "author"}
	if _, u := half.ownerView(); u != "visitor" {
		t.Fatalf("a half-set owner view resolved to %q; it must fall back", u)
	}

	// fleetView is the same rule and must not drift from it.
	fdb, fu := pub.fleetView()
	odb, ou := pub.ownerView()
	if fu != ou || fdb != odb {
		t.Fatalf("fleetView (%q) and ownerView (%q) disagree", fu, ou)
	}
}
