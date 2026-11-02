package main

import (
	"os"
	"strings"
	"testing"
)

// Serve checks the saved connections at start, so what is wrong with one is
// in the log before a call first meets it.
func TestStartupChecksSavedConnections(t *testing.T) {
	src, err := os.ReadFile("gohort.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "service.LogSelfCheck(global.db)") {
		t.Error("serve does not check the saved connections")
	}
	// A connection has no approval to settle at start: it is served, and
	// reached through the pages that reference it.
	if strings.Contains(s, "service.Settle(") {
		t.Error("serve still settles what is in use")
	}
}
