package main

import (
	"os"
	"strings"
	"testing"
)

// Saved connections are checked at start, after the migrations have moved
// them, so what the log names is what the instance will run with.
func TestStartupChecksSavedConnections(t *testing.T) {
	src, err := os.ReadFile("gohort.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	mig, check := strings.Index(s, "service.MigrateConnections(global.db)"), strings.Index(s, "service.LogSelfCheck(global.db)")
	if mig < 0 || check < mig {
		t.Error("startup does not check the saved connections after moving them")
	}
}
