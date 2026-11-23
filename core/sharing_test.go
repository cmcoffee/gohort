package core

import "testing"

// A record in an app whose records belong to whoever made them reaches its
// owner and nobody else, an administrator included; one with no owner is an
// administrator's, not everybody's.
func TestARecordIsItsOwnersAlone(t *testing.T) {
	for _, c := range []struct {
		owner, user string
		admin, want bool
	}{
		{"alice", "alice", false, true},
		{"alice", "bob", false, false},
		{"alice", "root", true, false},
		{"", "root", true, true},
		{"", "bob", false, false},
		{"", "", false, false},
		{"alice", "", false, false},
	} {
		if got := OwnsRecord(c.owner, c.user, c.admin); got != c.want {
			t.Errorf("OwnsRecord(%q, %q, admin=%v) = %v, want %v", c.owner, c.user, c.admin, got, c.want)
		}
	}
}
