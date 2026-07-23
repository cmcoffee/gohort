package core

import "testing"

// TestStatusCodeFromLine covers the fetch_via status normalization. The hook
// used to hand scripts the raw status LINE while fetch_url handed them an int,
// so the documented guard `if result["status"] != 200` silently failed on
// fetch_via — authored scripts ended up carrying
// `int(r['status'].split()[1]) if isinstance(r['status'], str) else r['status']`
// to paper over the difference.
func TestStatusCodeFromLine(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"HTTP 207 Multi-Status", 207},
		{"HTTP 200 OK", 200},
		{"HTTP 201 Created", 201},
		{"HTTP 404 Not Found", 404},
		{"HTTP 500 Internal Server Error", 500},
		{"207", 207},
		// Nothing to find → 0, which reads as "not 2xx" in the standard
		// guard rather than masquerading as success.
		{"", 0},
		{"no code here", 0},
	}
	for _, c := range cases {
		if got := statusCodeFromLine(c.line); got != c.want {
			t.Errorf("statusCodeFromLine(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

// TestStatusCodeFromLineIgnoresNonStatusNumbers: only a plausible HTTP status
// number counts, so a reason phrase carrying a digit can't be mistaken for the
// code.
func TestStatusCodeFromLineIgnoresNonStatusNumbers(t *testing.T) {
	if got := statusCodeFromLine("HTTP 42 Weird"); got != 0 {
		t.Errorf("42 is not a status code, got %d", got)
	}
	if got := statusCodeFromLine("HTTP 200 OK 7"); got != 200 {
		t.Errorf("first plausible code wins, got %d", got)
	}
}
