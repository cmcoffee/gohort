package customapps

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The owner shares an app with one person by name, no administrator: that
// person opens it, nobody else does, a name that is not an account is
// refused, unsharing ends it, and a deleted account leaves the list.
func TestAnOwnerSharesAnAppWithAPerson(t *testing.T) {
	T, _, _ := groupShareFixture(t)

	if err := T.setPersonShare("alice", "tally", "nobody", true); err == nil {
		t.Error("a name that is not an account was shared with")
	}
	if err := T.setPersonShare("alice", "tally", "alice", true); err == nil {
		t.Error("the owner shared it with themselves")
	}
	if err := T.setPersonShare("bob", "tally", "carol", true); err == nil {
		t.Error("someone who does not own the app shared it")
	}

	if err := T.setPersonShare("alice", "tally", "carol", true); err != nil {
		t.Fatal(err)
	}
	if _, owner, ok := T.resolveSpec("carol", "tally"); !ok || owner != "alice" {
		t.Errorf("carol, named, should open alice's app: ok=%v owner=%q", ok, owner)
	}
	if _, _, ok := T.resolveSpec("bob", "tally"); ok {
		t.Error("bob, not named and the app not shared with his group, opened it")
	}
	spec, _ := loadSpec("alice", "tally")
	if people := sharePeopleJSON(spec); !strings.Contains(people, `"with":["carol"]`) || !strings.Contains(people, `"bob"`) {
		t.Errorf("the dialog's people = %s", people)
	}

	// A deleted account leaves the list.
	forgetAppReader("carol")
	if spec, _ := loadSpec("alice", "tally"); len(spec.SharedWith) != 0 {
		t.Errorf("carol is still on the list after her account went: %v", spec.SharedWith)
	}

	// Unsharing ends it, and with nothing else sharing the app its slug
	// leaves the index.
	if err := T.setPersonShare("alice", "tally", "bob", true); err != nil {
		t.Fatal(err)
	}
	if err := T.setPersonShare("alice", "tally", "bob", false); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := T.resolveSpec("bob", "tally"); ok {
		t.Error("an unshared person still opened it")
	}
	if _, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); shared {
		t.Error("the slug index still carries an app nothing shares")
	}
}
