package main

import "testing"

// TestSetupDashboardURLIsSomethingYouCanOpen. The bind address and the URL are
// different strings: 0.0.0.0 and "" are things you LISTEN on. Printing the bind
// string verbatim is how a first run ends with an operator pasting
// "https://0.0.0.0:8181" into a browser and concluding the server never came up.
func TestSetupDashboardURLIsSomethingYouCanOpen(t *testing.T) {
	cases := []struct {
		addr, cert string
		selfSigned bool
		want       string
	}{
		{"127.0.0.1:8181", "", false, "http://127.0.0.1:8181"},
		{"127.0.0.1:8181", "", true, "https://127.0.0.1:8181"},
		{"0.0.0.0:8181", "", true, "https://localhost:8181"},
		{":8080", "", false, "http://localhost:8080"},
		{"[::]:9000", "", false, "http://localhost:9000"},
		{"gohort.example.com:443", "/etc/tls/x.pem", false, "https://gohort.example.com:443"},
	}
	for _, c := range cases {
		got := renderDashboardURL(c.addr, c.selfSigned, c.cert)
		if got != c.want {
			t.Errorf("addr=%q selfSigned=%v cert=%q → %q, want %q", c.addr, c.selfSigned, c.cert, got, c.want)
		}
	}
}
