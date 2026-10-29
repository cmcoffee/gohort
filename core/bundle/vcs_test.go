package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GUARD: a bundle made from a folder that is a checkout (a file store's case
// folder indexed as evidence, an upload with a working copy inside) leaves
// its version-control metadata out, where a clone's token lives.
func TestBundleIngestLeavesVersionControlOut(t *testing.T) {
	withMemStore(t)
	dir := t.TempDir()
	const secret = "tok-FAKE-0123456789"
	for rel, body := range map[string]string{
		".git/config":      "url = https://oauth2:" + secret + "@host-a/org/repo.git\n",
		"sub/.hg/hgrc":     "default = https://user:" + secret + "@host-a/hg\n",
		".svn/wc.db":       "https://" + secret + "@host-a/svn\n",
		"logs/app.log":     "2026-03-14T01:00:00Z INFO started\n",
		"notes/.gitignore": "*.tmp\n", // a file named like it is not the folder
	} {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open("u1", "b1").IngestFrom(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, bf := range Open("u1", "b1").Index() {
		paths = append(paths, bf.Path)
	}
	got := strings.Join(paths, ",")
	if strings.Contains(got, ".git/") || strings.Contains(got, ".hg/") || strings.Contains(got, ".svn/") || !strings.Contains(got, "logs/app.log") || !strings.Contains(got, "notes/.gitignore") {
		t.Errorf("indexed: %s", got)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: secret})
	if err != nil || res.Total != 0 {
		t.Errorf("the token is searchable: %+v, %v", res.Hits, err)
	}
}
