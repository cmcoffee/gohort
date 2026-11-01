package main

import (
	"os"
	"strings"
	"testing"
)

// Serve settles which connections pages reference, then checks the saved
// connections, so what the log names is what the instance will run with.
func TestStartupChecksSavedConnections(t *testing.T) {
	src, err := os.ReadFile("gohort.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	settle, check := strings.Index(s, "service.Settle(global.db)"), strings.Index(s, "service.LogSelfCheck(global.db)")
	if settle < 0 || check < settle {
		t.Error("serve does not check the saved connections after settling what references them")
	}
}
