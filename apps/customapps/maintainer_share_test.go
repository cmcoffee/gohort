package customapps

import (
	"strings"
	"testing"
)

// The owner names a maintainer on the Share dialog. Naming one shares the app
// with them; taking them off the maintainers leaves them able to use it;
// unsharing them, or deleting their account, drops them as a maintainer too.
// Only the owner names one.
func TestAnOwnerNamesAMaintainerWhoIsSharedTheApp(t *testing.T) {
	T, _, _ := groupShareFixture(t)

	if err := T.setMaintainer("alice", "tally", "nobody", true); err == nil {
		t.Error("a name that is not an account was made a maintainer")
	}
	if err := T.setMaintainer("alice", "tally", "alice", true); err == nil {
		t.Error("the owner was made a maintainer of their own app")
	}
	if err := T.setMaintainer("bob", "tally", "carol", true); err == nil {
		t.Error("someone who does not own the app named a maintainer")
	}

	if err := T.setMaintainer("alice", "tally", "carol", true); err != nil {
		t.Fatal(err)
	}
	spec, _ := loadSpec("alice", "tally")
	if !spec.MaintainedBy("carol") || !personShareReaches(spec, "carol") {
		t.Fatalf("carol should maintain the app and be shared it: maintainers=%v shared=%v", spec.Maintainers, spec.SharedWith)
	}
	if _, owner, ok := T.resolveSpec("carol", "tally"); !ok || owner != "alice" {
		t.Errorf("a maintainer cannot open the app: ok=%v owner=%q", ok, owner)
	}
	if m := shareMaintainersJSON(spec); !strings.Contains(m, `"with":["carol"]`) || !strings.Contains(m, `"bob"`) || strings.Contains(m, `"alice"`) {
		t.Errorf("the dialog's maintainers = %s", m)
	}

	// Off the maintainers, still a user.
	if err := T.setMaintainer("alice", "tally", "carol", false); err != nil {
		t.Fatal(err)
	}
	spec, _ = loadSpec("alice", "tally")
	if spec.MaintainedBy("carol") || !personShareReaches(spec, "carol") {
		t.Errorf("taking carol off the maintainers should leave her a user: maintainers=%v shared=%v", spec.Maintainers, spec.SharedWith)
	}

	// Unsharing drops a maintainer.
	if err := T.setMaintainer("alice", "tally", "carol", true); err != nil {
		t.Fatal(err)
	}
	if err := T.setPersonShare("alice", "tally", "carol", false); err != nil {
		t.Fatal(err)
	}
	if spec, _ = loadSpec("alice", "tally"); spec.MaintainedBy("carol") {
		t.Error("an unshared person is still a maintainer")
	}

	// A deleted account leaves the maintainers.
	if err := T.setMaintainer("alice", "tally", "bob", true); err != nil {
		t.Fatal(err)
	}
	forgetAppReader("bob")
	if spec, _ = loadSpec("alice", "tally"); spec.MaintainedBy("bob") || personShareReaches(spec, "bob") {
		t.Errorf("bob is still on the app after his account went: maintainers=%v shared=%v", spec.Maintainers, spec.SharedWith)
	}
}
