package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A dry run copies the database, runs the startup migrations on the copy,
// and reports what they changed there: a tier saved as service:<name> is
// shown renamed, and the database itself is not touched.
func TestADryRunReportsWithoutChanging(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "gohort.db")
	db, err := SecureDatabase(src)
	if err != nil {
		t.Fatal(err)
	}
	db.Set(LLMTable, "provider", "service:hosted")
	db.Close()
	sum := func() [32]byte {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(b)
	}
	before, root := sum(), RootDB

	var out bytes.Buffer
	if err := dryRunMigrations(src, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{"Nothing in it was changed", "\n  llm_config\n", `~ provider: "service:hosted" -> "llm:hosted"`, "+ core:connections:v3: runs, changing 1 record(s)"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if sum() != before {
		t.Error("the dry run changed the database itself")
	}
	if RootDB != root {
		t.Error("the dry run left RootDB on its copy")
	}
}

// A database a running gohort holds is not copied: it says to stop it.
func TestADryRunWaitsForGohortToStop(t *testing.T) {
	src := filepath.Join(t.TempDir(), "gohort.db")
	held, err := SecureDatabase(src)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := snapshotDB(src, filepath.Join(t.TempDir(), "copy.db")); err == nil || !strings.Contains(err.Error(), "stop gohort") {
		t.Errorf("a held database was copied: %v", err)
	}
}

// A value is shown as text or a list, never when it is encrypted, and by its
// size otherwise.
func TestADryRunNeverShowsAnEncryptedValue(t *testing.T) {
	enc := func(v any) []byte {
		var b bytes.Buffer
		gob.NewEncoder(&b).Encode(v)
		return append([]byte{0}, b.Bytes()...)
	}
	secret := append([]byte{1}, enc("sk-live-123")[1:]...)
	for raw, want := range map[string]string{
		string(enc("llm:x")):                         `"llm:x"`,
		string(enc([]string{"a", "b"})):              `["a" "b"]`,
		string(enc(true)):                            "true",
		string(secret):                               "(encrypted)",
		string(enc(struct{ A, B string }{"x", "y"})): "(record,",
	} {
		if got := shownValue([]byte(raw)); !strings.HasPrefix(got, want) {
			t.Errorf("shown as %q, want %q", got, want)
		}
	}
}
