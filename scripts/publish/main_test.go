package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/selfupdate"
)

type fixture struct {
	dir, keyFile, keyList, dist string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{dir: dir, keyFile: filepath.Join(dir, "home", "release.key"), keyList: filepath.Join(dir, "release_keys.txt"), dist: filepath.Join(dir, "distro", "gohort")}
	os.WriteFile(f.keyList, []byte("# keys\n"), 0o644)
	os.MkdirAll(f.dist, 0o755)
	if _, err := makeKey(f.keyFile, f.keyList); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) stage(t *testing.T, version string) string {
	t.Helper()
	stage := filepath.Join(f.dir, "stage-"+version)
	for _, p := range []string{"linux-amd64", "windows-amd64"} {
		bin := "gohort"
		if strings.HasPrefix(p, "windows") {
			bin += ".exe"
		}
		os.MkdirAll(filepath.Join(stage, p), 0o755)
		os.WriteFile(filepath.Join(stage, p, bin), []byte("binary "+version+" "+p), 0o755)
		os.WriteFile(filepath.Join(stage, p, "THIRD_PARTY_NOTICES"), []byte("notices "+p), 0o644)
	}
	os.WriteFile(filepath.Join(stage, "LICENSE"), []byte("license"), 0o644)
	return stage
}

func (f fixture) publish(t *testing.T, version string) (map[string]selfupdate.File, error) {
	t.Helper()
	priv, err := loadKey(f.keyFile, f.keyList)
	if err != nil {
		t.Fatal(err)
	}
	r := release{stage: f.stage(t, version), dist: f.dist, version: version, commit: "abc1234", product: "gohort",
		priv: priv, keep: 3, now: func() time.Time { return time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC) }}
	return r.publish()
}

// What publish writes is what a server's updater reads: the list checks
// against the built-in key list, and each binary against the list.
func TestAPublishedReleaseIsWhatTheUpdaterTakes(t *testing.T) {
	f := newFixture(t)
	if _, err := f.publish(t, "0.9.380"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dist, "version.txt")); string(b) != "0.9.380" {
		t.Errorf("version.txt is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dist, "0.9.380", "linux-amd64", "THIRD_PARTY_NOTICES")); string(b) != "notices linux-amd64" {
		t.Errorf("the platform's notices are not beside its binary: %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.dist, "0.9.380", "LICENSE")); err != nil {
		t.Error("the license is not in the release folder")
	}
	if left, _ := filepath.Glob(filepath.Join(f.dist, "*", ".publish-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(f.dist))))
	defer srv.Close()
	lb, _ := os.ReadFile(f.keyList)
	keys, err := selfupdate.ParseKeys(string(lb))
	if err != nil || len(keys) != 1 {
		t.Fatalf("the key list reads as %d keys: %v", len(keys), err)
	}
	src := selfupdate.Source{URL: srv.URL + "/gohort", Keys: keys, Product: "gohort"}
	m, err := src.Latest(context.Background())
	if err != nil || m.Version != "0.9.380" || m.Commit != "abc1234" {
		t.Fatalf("latest %+v %v", m, err)
	}
	if m.Files["windows-amd64"].Path != "0.9.380/windows-amd64/gohort.exe" {
		t.Errorf("the windows path is %q", m.Files["windows-amd64"].Path)
	}
	path, err := src.Download(context.Background(), m, "linux-amd64", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "binary 0.9.380 linux-amd64" {
		t.Errorf("downloaded %q", b)
	}
}

func TestPublishRefusesWhatWouldConfuseAServer(t *testing.T) {
	f := newFixture(t)
	if _, err := f.publish(t, "0.9.380"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.publish(t, "0.9.380"); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Errorf("the same version again: %v", err)
	}
	if _, err := f.publish(t, "0.9.379"); err == nil {
		t.Error("an older version over a newer one")
	}
	if _, err := f.publish(t, "../../etc"); err == nil {
		t.Error("a version that is a path")
	}
	// A key the built-in list does not have.
	os.WriteFile(f.keyList, []byte("# emptied\n"), 0o644)
	if _, err := loadKey(f.keyFile, f.keyList); err == nil || !strings.Contains(err.Error(), "is not in") {
		t.Errorf("a key servers cannot check: %v", err)
	}
	os.Chmod(f.keyFile, 0o644)
	if _, err := loadKey(f.keyFile, f.keyList); err == nil || !strings.Contains(err.Error(), "read by others") {
		t.Errorf("a key others can read: %v", err)
	}
	if _, err := makeKey(f.keyFile, f.keyList); err == nil || !strings.Contains(err.Error(), "never written over") {
		t.Errorf("a second key over the first: %v", err)
	}
	r := release{stage: f.dir, dist: filepath.Join(f.dir, "not-mounted"), version: "0.9.390"}
	if _, err := r.publish(); err == nil || !strings.Contains(err.Error(), "mounted") {
		t.Errorf("a missing dist folder: %v", err)
	}
}

func TestTheKeyFileIsTheOwnersOnly(t *testing.T) {
	f := newFixture(t)
	st, err := os.Stat(f.keyFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key file is %v %v", st.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(f.keyList); strings.Count(string(b), "# key ") != 1 {
		t.Errorf("the key list is %q", b)
	}
}

func TestOldReleasesArePrunedPastTheNewestThree(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(filepath.Join(f.dist, "docs"), 0o755)
	for _, v := range []string{"0.9.377", "0.9.378", "0.9.379", "0.9.380", "0.9.381"} {
		if _, err := f.publish(t, v); err != nil {
			t.Fatal(v, err)
		}
	}
	var got []string
	ents, _ := os.ReadDir(f.dist)
	for _, e := range ents {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	if strings.Join(got, " ") != "0.9.379 0.9.380 0.9.381 docs" {
		t.Errorf("left %v", got)
	}
}
