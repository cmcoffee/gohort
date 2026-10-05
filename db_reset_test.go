package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A database the padlock does not open is refused, saying the likely cause
// and both ways forward; a db_locker written this start is named as the
// cause, since a new one never opens an existing database.
func TestAPadlockMismatchIsRefusedWithAWayForward(t *testing.T) {
	err := padlockRefusal("/data/gohort.db", false)
	for _, want := range []string{"refusing to open /data/gohort.db", "gohort.ini was replaced", "put back the gohort.ini", resetSecretsEnv + "=1", "copied aside first"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, err)
		}
	}
	if err := padlockRefusal("/data/gohort.db", true); !strings.Contains(err.Error(), "had no [do_not_modify] db_locker line") {
		t.Errorf("a new locker should be named as the cause:\n%s", err)
	}
}

// The reset copies the database aside before clearing anything, and never
// overwrites an earlier copy.
func TestTheDatabaseIsCopiedAsideFirst(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gohort.db")
	if err := os.WriteFile(file, []byte("secrets inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 21, 30, 0, 0, time.UTC)
	saved, err := copyAside(file, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved != file+".before-reset-20261004-213000" {
		t.Fatalf("saved at %s", saved)
	}
	if got, _ := os.ReadFile(saved); string(got) != "secrets inside" {
		t.Fatalf("copy holds %q", got)
	}
	if _, err := copyAside(file, now); err == nil {
		t.Fatal("a second copy at the same time overwrote the first")
	}
}
