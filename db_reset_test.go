package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/cfg"
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
	if err := padlockRefusal("/data/gohort.db", true); !strings.Contains(err.Error(), "has no [do_not_modify] db_locker line") || !strings.Contains(err.Error(), "network address") {
		t.Errorf("a missing line should be named as the cause:\n%s", err)
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

// An ini with no db_locker line gets the padlock every existing database was
// opened with (the machine's MAC), and the line is SAVED to the file, so the
// next start reads it from the ini rather than deriving it again: the
// database moves with its ini, not with the machine. v0.7.363 gave each
// start fresh random bytes that were never saved, and no existing database
// opened.
func TestAMissingPadlockLineIsTheMACAndIsSaved(t *testing.T) {
	mac := get_mac_addr()
	if len(mac) == 0 {
		t.Skip("no network interface with a hardware address here")
	}
	dir := t.TempDir()
	ini := filepath.Join(dir, "gohort.ini")
	if err := os.WriteFile(ini, []byte("[web]\naddr = :8181\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _db_locker_created = false })
	store := &cfg.Store{}
	if err := store.File(ini); err != nil {
		t.Fatal(err)
	}

	got := unlockDB(store)
	if string(got) != string(mac) {
		t.Fatal("a missing db_locker line did not give the MAC padlock existing databases use")
	}
	raw, _ := os.ReadFile(ini)
	if !strings.Contains(string(raw), "db_locker") || !strings.Contains(string(raw), "addr = :8181") {
		t.Fatalf("the padlock was not saved beside the rest of the ini:\n%s", raw)
	}

	// The next start reads it back from the file.
	store2 := &cfg.Store{}
	if err := store2.File(ini); err != nil {
		t.Fatal(err)
	}
	if again := unlockDB(store2); string(again) != string(mac) {
		t.Fatal("the saved padlock did not read back as the same bytes")
	}
}
