package anvil

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Download changes is everything that differs from the last commit: an edit,
// a new file, a new binary file and a removal, which git apply on a copy of
// the last commit turns into the same tree. The project's own index is not
// touched by making it.
func TestDownloadChangesCarriesEveryChangeBack(t *testing.T) {
	origin := t.TempDir()
	initRepo(t, origin)
	mustWrite(t, filepath.Join(origin, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(origin, "old.txt"), "going\n")
	commitAll(t, origin, "c0")
	home := filepath.Join(t.TempDir(), "home")
	if out, err := exec.Command("git", "clone", "-q", origin, home).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	mustWrite(t, filepath.Join(origin, "a.go"), "package a\n\nfunc A() {}\n")
	mustWrite(t, filepath.Join(origin, "dir", "new.go"), "package dir\n")
	if err := os.WriteFile(filepath.Join(origin, "blob.bin"), []byte{0, 1, 2, 255, 0, 7}, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(origin, "old.txt"))

	patch, err := workingPatch(t.Context(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if staged, _ := runGit(origin, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("making the patch staged files in the project: %q", staged)
	}
	pf := filepath.Join(t.TempDir(), "c.patch")
	os.WriteFile(pf, patch, 0o644)
	if out, err := exec.Command("git", "-C", home, "apply", pf).CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v %s\n%s", err, out, patch)
	}
	for _, f := range []string{"a.go", "dir/new.go", "blob.bin"} {
		want, _ := os.ReadFile(filepath.Join(origin, f))
		got, err := os.ReadFile(filepath.Join(home, f))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s after apply: %q, want %q (%v)", f, got, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "old.txt")); err == nil {
		t.Error("the removal did not carry over")
	}

	clean := t.TempDir()
	initRepo(t, clean)
	mustWrite(t, filepath.Join(clean, "x"), "x\n")
	commitAll(t, clean, "c")
	if patch, err := workingPatch(t.Context(), clean); err != nil || len(patch) != 0 {
		t.Errorf("a clean project: %d bytes, %v", len(patch), err)
	}
	if _, err := workingPatch(t.Context(), t.TempDir()); err == nil {
		t.Error("a project outside git made a patch")
	}
}

// The download answers someone who may open the project with a file to save,
// and says so plainly when nothing differs.
func TestDownloadChangesIsAFileToSave(t *testing.T) {
	T, serve := bundleRoutes(t)
	udb := userDBFor(T, "alice")
	p := Project{ID: "d1", Name: "My Project!", Kind: KindClone, Owner: "alice", RepoURL: "https://example.test/r.git"}
	T.saveProject(udb, p)
	base, _ := p.baseDir()
	os.MkdirAll(base, 0o700)
	initRepo(t, base)
	mustWrite(t, filepath.Join(base, "f"), "one\n")
	commitAll(t, base, "c")
	if w := serve("alice", "GET", "/api/project/d1/patch", nil, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "nothing differs") {
		t.Errorf("nothing changed: %d %s", w.Code, w.Body.String())
	}
	mustWrite(t, filepath.Join(base, "f"), "two\n")
	w := serve("alice", "GET", "/api/project/d1/patch", nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "+two") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), `filename="My-Project-`) {
		t.Errorf("download: %d %q %s", w.Code, w.Header().Get("Content-Disposition"), w.Body.String())
	}
	if w := serve("root", "GET", "/api/project/d1/patch", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("someone who cannot open it: %d", w.Code)
	}
}

func userDBFor(T *Anvil, user string) Database { return UserDB(T.DB, user) }
