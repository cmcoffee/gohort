package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/cfg"
	"github.com/cmcoffee/snugforge/kvlite"
)

var (
	fakeMAC = []byte{0x02, 0x42, 0xac, 0x11, 0x00, 0x02}
	fakeID  = func(string) ([]byte, error) { return []byte("machine-id-for-gohort"), nil }
	noID    = func(string) ([]byte, error) { return nil, errors.New("no machine id") }
)

// iniWith is an ini file holding body, and a store reading it.
func iniWith(t *testing.T, body string) (*cfg.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gohort.ini")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &cfg.Store{}
	if err := store.File(path); err != nil {
		t.Fatal(err)
	}
	return store, path
}

// lock = machine locks to the machine ID and writes nothing to the ini; the
// old padlocks stay as fallbacks to open and move an existing database.
func TestMachineLockUsesTheMachineID(t *testing.T) {
	store, path := iniWith(t, "[web]\naddr = :8181\n[database]\nlock = machine\n")
	plan, note := planPadlocks(store, path, fakeID, fakeMAC)
	if plan.mode != "machine" || string(plan.target) != "machine-id-for-gohort" || note != "" {
		t.Fatalf("plan %+v, note %q", plan, note)
	}
	if len(plan.fallbacks) != 2 || string(plan.fallbacks[1]) != string(fakeMAC) {
		t.Fatalf("fallbacks = %v", plan.fallbacks)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "db_locker") {
		t.Fatal("a machine lock wrote a padlock to the ini")
	}
}

// Portable, the default (no [database] section at all), keeps a random
// padlock in the ini, SAVED and read back, so the next start (a new store
// reading the file) gets the same one.
func TestPortableIsTheDefaultAndSavesItsPadlock(t *testing.T) {
	store, path := iniWith(t, "[web]\naddr = :8181\n")
	plan, _ := planPadlocks(store, path, fakeID, fakeMAC)
	if plan.mode != "portable" || len(plan.target) != 32 {
		t.Fatalf("plan %+v", plan)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "db_locker") || !strings.Contains(string(raw), "addr = :8181") {
		t.Fatalf("not saved beside the rest:\n%s", raw)
	}
	again := &cfg.Store{}
	again.File(path)
	plan2, _ := planPadlocks(again, path, fakeID, fakeMAC)
	if string(plan2.target) != string(plan.target) {
		t.Fatal("the next start got a different padlock")
	}
}

// No machine ID means portable, said; an ini that cannot be written then
// keeps the MAC rather than an unsaved random padlock that opens nothing on
// the next start.
func TestNoMachineIDFallsBackSafely(t *testing.T) {
	store, path := iniWith(t, "[database]\nlock = machine\n")
	plan, note := planPadlocks(store, path, noID, fakeMAC)
	if plan.mode != "portable" || !strings.Contains(note, "no usable machine ID") {
		t.Fatalf("plan %+v note %q", plan, note)
	}
	unwritable := &cfg.Store{}
	unwritable.File(filepath.Join(t.TempDir(), "missing-dir", "gohort.ini"))
	unwritable.Set("database", "lock", "machine")
	plan, note = planPadlocks(unwritable, filepath.Join(t.TempDir(), "missing-dir", "gohort.ini"), noID, fakeMAC)
	if plan.mode != "network address" || string(plan.target) != string(fakeMAC) || !strings.Contains(note, "Could not save") {
		t.Fatalf("unwritable ini: plan %+v note %q", plan, note)
	}
}

// An existing database under the MAC (every database before machine IDs) is
// copied aside, moved to the machine ID with its values intact, and then
// opens with the machine ID alone; switching to portable moves it again.
func TestAnExistingDatabaseMovesToTheMachineID(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gohort.db")
	st, err := kvlite.Open(file, fakeMAC...)
	if err != nil {
		t.Fatal(err)
	}
	st.CryptSet("secrets", "api_key", "s3cr3t")
	st.Close()

	store, path := iniWith(t, "[database]\nlock = machine\n")
	plan, _ := planPadlocks(store, path, fakeID, fakeMAC)
	db, err := secureDatabaseWith(file, plan)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	db.Get("secrets", "api_key", &v)
	db.Close()
	if v != "s3cr3t" {
		t.Fatalf("secret after the move = %q", v)
	}
	if copies, _ := filepath.Glob(file + ".*"); len(copies) != 0 {
		t.Fatalf("copies made: %v", copies)
	}
	if _, err := kvlite.Open(file, fakeMAC...); err != kvlite.ErrBadPadlock {
		t.Fatalf("the MAC still opens it: %v", err)
	}
	if st, err := kvlite.Open(file, []byte("machine-id-for-gohort")...); err != nil {
		t.Fatalf("the machine ID does not open it: %v", err)
	} else {
		st.Close()
	}

	portable, ppath := iniWith(t, "[database]\nlock = portable\n")
	pplan, _ := planPadlocks(portable, ppath, fakeID, fakeMAC)
	db, err = secureDatabaseWith(file, pplan)
	if err != nil {
		t.Fatal(err)
	}
	v = ""
	db.Get("secrets", "api_key", &v)
	db.Close()
	if v != "s3cr3t" {
		t.Fatalf("secret after moving to portable = %q", v)
	}
	if st, err := kvlite.Open(file, pplan.target...); err != nil {
		t.Fatalf("the saved padlock does not open it: %v", err)
	} else {
		st.Close()
	}
}

// A database no padlock this machine has opens gets its secrets cleared and
// opens, keeping its plain values, under the target padlock.
func TestNoPadlockOpensItResets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gohort.db")
	st, _ := kvlite.Open(file, []byte("somewhere-else")...)
	st.CryptSet("secrets", "api_key", "lost")
	st.Set("plain", "name", "kept")
	st.Close()
	store, path := iniWith(t, "[web]\n")
	plan, _ := planPadlocks(store, path, fakeID, fakeMAC)
	db, err := secureDatabaseWith(file, plan)
	if err != nil {
		t.Fatal(err)
	}
	var secret, name string
	db.Get("secrets", "api_key", &secret)
	db.Get("plain", "name", &name)
	db.Close()
	if secret != "" || name != "kept" {
		t.Fatalf("secret %q, plain %q", secret, name)
	}
	if st, err := kvlite.Open(file, plan.target...); err != nil {
		t.Fatalf("after the reset the target does not open it: %v", err)
	} else {
		st.Close()
	}
	if copies, _ := filepath.Glob(file + ".*"); len(copies) != 0 {
		t.Fatalf("copies made: %v", copies)
	}
}
