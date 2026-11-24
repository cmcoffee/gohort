package servitor

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// writeLog records which kind of write reached each table.
type writeLog struct {
	mu     sync.Mutex
	plain  map[string]int
	sealed map[string]int
}

func (l *writeLog) add(sealed bool, table string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if sealed {
		l.sealed[table]++
	} else {
		l.plain[table]++
	}
}

// recordingStore is a store that notes, for every write, whether it was
// encrypted. Sub-stores note into the same log, since a person's systems live
// in one.
type recordingStore struct {
	kvlite.Store
	log *writeLog
}

func newRecordingStore() (*recordingStore, *writeLog) {
	l := &writeLog{plain: map[string]int{}, sealed: map[string]int{}}
	return &recordingStore{Store: kvlite.MemStore(), log: l}, l
}

func (s *recordingStore) Sub(name string) kvlite.Store {
	return &recordingStore{Store: s.Store.Sub(name), log: s.log}
}

func (s *recordingStore) Bucket(name string) kvlite.Store {
	return &recordingStore{Store: s.Store.Bucket(name), log: s.log}
}

func (s *recordingStore) Set(table, key string, value interface{}) error {
	s.log.add(false, table)
	return s.Store.Set(table, key, value)
}

func (s *recordingStore) CryptSet(table, key string, value interface{}) error {
	s.log.add(true, table)
	return s.Store.CryptSet(table, key, value)
}

// Saving a system writes its record encrypted, the credentials with it, and
// it still loads as it was saved.
func TestASavedSystemIsWrittenEncrypted(t *testing.T) {
	aliceAndBob(t)
	store, log := newRecordingStore()
	app := &Servitor{}
	app.DB = &DBase{Store: store}
	// RepoFiles is set so that no save here starts a clone; the form edits an
	// existing system, which is the save path every edit takes.
	saveAppliance(UserDB(app.DB, "alice"), "box", Appliance{ID: "box", Name: "box", Type: "ssh", Host: "box.example.com", Owner: "alice", RepoFiles: 1})
	log.sealed[applianceTable] = 0
	w := httptest.NewRecorder()
	routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", "alice",
		`{"id":"box","type":"ssh","name":"box","host":"box.example.com","password":"pw-secret","sudo_password":"sudo-secret","root_password":"root-secret"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("save answered %d %s", w.Code, w.Body.String())
	}
	if log.plain[applianceTable] != 0 || log.sealed[applianceTable] == 0 {
		t.Errorf("a system's record must be written encrypted: plain=%d encrypted=%d", log.plain[applianceTable], log.sealed[applianceTable])
	}
	var a Appliance
	if !UserDB(app.DB, "alice").Get(applianceTable, "box", &a) || a.Password != "pw-secret" || a.SudoPassword != "sudo-secret" || a.RootPassword != "root-secret" {
		t.Errorf("the encrypted record does not load as saved: %+v", a)
	}
}

// On disk: a password saved through the system form is not in the database
// file as written.
func TestASystemsPasswordIsNotInTheDatabaseFile(t *testing.T) {
	aliceAndBob(t)
	path := filepath.Join(t.TempDir(), "servitor.db")
	store, err := kvlite.Open(path, []byte("a test padlock, 32 bytes or more...")...)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &Servitor{}
	app.DB = &DBase{Store: store}
	const marker = "pw-marker-6f1e2d9c"
	saveAppliance(UserDB(app.DB, "alice"), "box", Appliance{ID: "box", Name: "box", Type: "ssh", Host: "box.example.com", Owner: "alice", Password: marker})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Error("the system's password is in the database file as written")
	}
	var a Appliance
	if !UserDB(app.DB, "alice").Get(applianceTable, "box", &a) || a.Password != marker {
		t.Errorf("the password does not load back: %+v", a)
	}
}

// Records written plain before this are re-written encrypted, once per
// person, and keep everything they held.
func TestStoredSystemsAreReEncryptedOnce(t *testing.T) {
	aliceAndBob(t)
	store, log := newRecordingStore()
	db := &DBase{Store: store}
	udb := UserDB(db, "alice")
	udb.Set(applianceTable, "box", Appliance{ID: "box", Name: "box", Type: "ssh", Host: "box.example.com", Owner: "alice", Password: "pw-secret"})
	udb.Set(applianceTable, "code", Appliance{ID: "code", Name: "code", Type: "repo", Owner: "alice", RepoToken: "tok-secret"})
	if n := encryptUserSystems(udb); n != 2 {
		t.Fatalf("re-wrote %d record(s), want 2", n)
	}
	if log.sealed[applianceTable] != 2 {
		t.Errorf("each record must be re-written encrypted: encrypted=%d", log.sealed[applianceTable])
	}
	var a, c Appliance
	udb.Get(applianceTable, "box", &a)
	udb.Get(applianceTable, "code", &c)
	if a.Password != "pw-secret" || a.Host != "box.example.com" || c.RepoToken != "tok-secret" {
		t.Errorf("a re-written record lost what it held: %+v %+v", a, c)
	}
	if n := encryptUserSystems(udb); n != 0 || log.sealed[applianceTable] != 2 {
		t.Errorf("a second run re-wrote again: n=%d encrypted=%d", n, log.sealed[applianceTable])
	}
}

// Nothing in this package writes a system's record but saveAppliance: a plain
// Set on applianceTable puts its credentials in the file as they are.
func TestOnlySaveApplianceWritesASystem(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "Set(applianceTable") && !strings.Contains(line, "CryptSet(applianceTable") {
				t.Errorf("%s:%d writes a system's record unencrypted; use saveAppliance", f, i+1)
			}
		}
	}
}
