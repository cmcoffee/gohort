package anvil

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// What "@" offers: a name starting with what was typed first, then one
// containing it, then a path containing it, then its letters in order; the
// shallower first among equals. .git and ignored files are not offered.
func TestMentionsOfferTheBestFilesFirst(t *testing.T) {
	base := t.TempDir()
	for _, f := range []string{"main.go", "cmd/mainline/run.go", "lib/domain.go", "lib/m_a_i_n.txt", "docs/readme.md", ".git/config", "build/out.bin"} {
		mustWrite(t, filepath.Join(base, f), "x\n")
	}
	mustWrite(t, filepath.Join(base, ".gitignore"), "build/\n")
	c := &toolCtx{project: Project{ID: "m1"}, base: base}
	got := strings.Join(c.matchingFiles("Main"), ",")
	if got != "main.go,lib/domain.go,cmd/mainline/run.go,lib/m_a_i_n.txt" {
		t.Errorf("@Main offers %s", got)
	}
	all := strings.Join(c.matchingFiles(""), ",")
	if strings.Contains(all, ".git/") || strings.Contains(all, "build/") || !strings.HasPrefix(all, "main.go,.gitignore,") {
		t.Errorf("@ offers %s", all)
	}
	if got := c.matchingFiles("zzz"); len(got) != 0 {
		t.Errorf("@zzz offers %v", got)
	}
}

// The endpoint answers for a project the person may open, and with nothing
// for one they cannot name.
func TestTheMentionsEndpointListsAProjectsFiles(t *testing.T) {
	T, serve := bundleRoutes(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "server.go"), "package x\n")
	T.saveProject(UserDB(T.DB, "root"), Project{ID: "r9", Name: "R", Kind: KindRoot, Root: root})
	var got []mentionOption
	w := serve("root", "GET", "/api/mentions?project_id=r9&q=serv", nil, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Value != "server.go" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = serve("alice", "GET", "/api/mentions?project_id=nope&q=serv", nil, "")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("an unknown project offers %s", w.Body.String())
	}
}
