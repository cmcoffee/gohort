package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Every database this opens has its CFB secrets re-encrypted with GCM, in the
// background (kvlite.UpgradeCipher, tested in snugforge).
func TestSecureDatabaseUpgradesTheCipher(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func secureDatabaseWith(")
	if i < 0 {
		t.Fatal("secureDatabaseWith not found")
	}
	body := s[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "go upgradeDatabaseCipher(file, st)") {
		t.Error("a database opened here no longer has its secrets re-encrypted with GCM")
	}
}

// The re-encryption pass says how far through the tables it is on its tick,
// and a pass that showed progress ends with a line of its own even when it
// found nothing, so the last thing in the log is never a stale tick.
func TestCipherProgressSaysHowFarAndEnds(t *testing.T) {
	clock := time.Unix(1000, 0)
	var lines []string
	c := &cipherProgress{file: "x.db", start: clock, last: clock,
		now:  func() time.Time { return clock },
		logf: func(f string, a ...interface{}) { lines = append(lines, fmt.Sprintf(f, a...)) }}

	c.tick(kvlite.UpgradeProgress{Tables: 312, TablesDone: 3})
	if len(lines) != 0 {
		t.Fatalf("logged before the tick: %q", lines)
	}
	clock = clock.Add(3 * time.Second)
	c.tick(kvlite.UpgradeProgress{Tables: 312, TablesDone: 140, Upgraded: 16})
	if len(lines) != 1 || !strings.Contains(lines[0], "140 of 312 tables done, 16 secret(s) so far (3s)") {
		t.Fatalf("progress line = %q", lines)
	}
	c.finish(0, nil)
	if len(lines) != 2 || !strings.Contains(lines[1], "no stored secrets were left") {
		t.Errorf("a pass that showed progress and found nothing ended with %q", lines)
	}

	quiet := &cipherProgress{file: "x.db", start: clock, last: clock, now: func() time.Time { return clock },
		logf: func(f string, a ...interface{}) { t.Errorf("a quick pass with nothing to do logged: "+f, a...) }}
	quiet.tick(kvlite.UpgradeProgress{Tables: 2, TablesDone: 2})
	quiet.finish(0, nil)
}
