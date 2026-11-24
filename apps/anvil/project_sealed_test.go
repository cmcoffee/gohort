package anvil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// sealCounter notes how many writes to each table were encrypted.
type sealCounter struct {
	kvlite.Store
	plain, sealed map[string]int
}

func (s *sealCounter) Sub(name string) kvlite.Store {
	return &sealCounter{Store: s.Store.Sub(name), plain: s.plain, sealed: s.sealed}
}

func (s *sealCounter) Set(table, key string, value interface{}) error {
	s.plain[table]++
	return s.Store.Set(table, key, value)
}

func (s *sealCounter) CryptSet(table, key string, value interface{}) error {
	s.sealed[table]++
	return s.Store.CryptSet(table, key, value)
}

// A project is saved encrypted, its tokens with it, and loads as saved; a
// project written plain before is re-written encrypted once.
func TestProjectsAreWrittenEncrypted(t *testing.T) {
	store := &sealCounter{Store: kvlite.MemStore(), plain: map[string]int{}, sealed: map[string]int{}}
	app := &Anvil{}
	app.DB = &DBase{Store: store}
	app.saveProject(nil, Project{ID: "r1", Name: "root", Kind: KindRoot, PushToken: "push-secret"})
	if store.plain[projectTable] != 0 || store.sealed[projectTable] != 1 {
		t.Errorf("a project must be written encrypted: plain=%d encrypted=%d", store.plain[projectTable], store.sealed[projectTable])
	}
	app.DB.Set(projectTable, "old", Project{ID: "old", Name: "old", Kind: KindRoot, Token: "clone-secret"})
	if n := encryptProjectsIn(app.DB); n != 2 || store.sealed[projectTable] != 3 {
		t.Errorf("re-wrote %d (encrypted writes %d), want both projects re-written", n, store.sealed[projectTable])
	}
	var p Project
	if !app.DB.Get(projectTable, "old", &p) || p.Token != "clone-secret" {
		t.Errorf("a re-written project lost its token: %+v", p)
	}
	if n := encryptProjectsIn(app.DB); n != 0 {
		t.Errorf("a second run re-wrote %d again", n)
	}
}

// Nothing in this package writes a project but saveProject and the one-time
// pass: a plain Set on projectTable puts its tokens in the file as they are.
func TestOnlyEncryptedWritesReachProjects(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "Set(projectTable") && !strings.Contains(line, "CryptSet(projectTable") {
				t.Errorf("%s:%d writes a project unencrypted; use saveProject", f, i+1)
			}
		}
	}
}
