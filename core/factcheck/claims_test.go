package factcheck

import "testing"

// The line between "a few sentences are unsourced" and "the report as a whole
// is unverified". A short ledger is too small to judge as a whole, and exactly
// half is not past the line.
func TestAuditFailed(t *testing.T) {
	cases := []struct {
		name               string
		unsupported, total int
		want               bool
	}{
		{"most claims untraced", 24, 32, true},
		{"exactly half", 16, 32, false},
		{"a few untraced", 3, 32, false},
		{"too short to judge", 4, 5, false},
		{"empty", 0, 0, false},
	}
	for _, c := range cases {
		if got := AuditFailed(c.unsupported, c.total); got != c.want {
			t.Errorf("%s: AuditFailed(%d, %d) = %v, want %v", c.name, c.unsupported, c.total, got, c.want)
		}
	}
}
