package orchestrate

import "testing"

// TestWatchProbeHTTPError is the heart of the bridge hard-fail verify: the
// underlying dispatch returns "HTTP <code> <text>\n<body>" as a NORMAL result
// even on 4xx (transport succeeded), so a 401/404 must be caught by inspecting
// the body, not the Go error — otherwise a broken source sails onto a schedule.
func TestWatchProbeHTTPError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"401 body", "HTTP 401 Unauthorized\n{\"error\":\"no key\"}", "HTTP 401"},
		{"404", "HTTP 404 Not Found\n", "HTTP 404"},
		{"500", "HTTP 500 Internal Server Error\nboom", "HTTP 500"},
		{"200 ok", "HTTP 200 OK\n{\"posts\":[]}", ""},
		{"204 ok", "HTTP 204 No Content\n", ""},
		{"piped json (no status line)", "[{\"id\":1}]", ""},
		{"empty", "", ""},
		{"leading space then 403", "  HTTP 403 Forbidden\nnope", "HTTP 403"},
	}
	for _, c := range cases {
		if got := watchProbeHTTPError(c.in); got != c.want {
			t.Errorf("%s: watchProbeHTTPError(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
