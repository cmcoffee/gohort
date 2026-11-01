package main

import (
	"os"
	"strings"
	"testing"
)

// Saved connections are checked at start, after the migrations have moved
// them, so what the log names is what the instance will run with; serve
// runs that list, and the dry run runs the same one.
func TestStartupChecksSavedConnections(t *testing.T) {
	read := func(f string) string {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(src)
	}
	s := read("migrate_dry_run.go")
	mig, check := strings.Index(s, "service.MigrateConnections(db)"), strings.Index(s, "service.LogSelfCheck(db)")
	if mig < 0 || check < mig {
		t.Error("the startup migrations do not check the saved connections after moving them")
	}
	if !strings.Contains(read("gohort.go"), "runStartupMigrations(global.db)") {
		t.Error("serve no longer runs the startup migrations")
	}
	// The dry run runs what serve does: the list, and the tool releases
	// init_database runs before it.
	if !strings.Contains(s, "MigrateToolReleases(db) // init_database runs it before the rest\n\trunStartupMigrations(db)") ||
		!strings.Contains(read("config.go"), "MigrateToolReleases(global.db)") {
		t.Error("the dry run no longer runs the migrations serve does")
	}
}
