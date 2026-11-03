package orchestrate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A link is built from where the app is actually mounted: the usual path, a
// different one, and none at all (the app at the server root).
func TestLinksFollowTheMountPath(t *testing.T) {
	prev := orchMount
	t.Cleanup(func() { orchMount = prev })
	for mount, want := range map[string]string{"/orchestrate": "/orchestrate/api/machines", "/agents2": "/agents2/api/machines", "": "/api/machines"} {
		orchMount = mount
		if got := orchPath("api/machines"); got != want {
			t.Errorf("mounted at %q: %q, want %q", mount, got, want)
		}
		if got := orchPath("/api/machines"); got != want {
			t.Errorf("mounted at %q, a leading slash: %q, want %q", mount, got, want)
		}
	}
	orchMount = "/agents2"
	if got := orchJS("fetch(ORCH_BASE + '/api/machines/')"); got != `fetch("/agents2" + '/api/machines/')` {
		t.Errorf("a script constant: %s", got)
	}
}

// No link in the app is written as "../" (right on one depth of page only) or
// as "/orchestrate/..." (wrong whenever the app is mounted elsewhere). The
// one exception reads links stored by an older version.
func TestNoLinkIsWrittenRelativeToAParentOrToAFixedMount(t *testing.T) {
	bad := regexp.MustCompile(`["'](\.\./api/|/orchestrate/)`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "agents_migrations.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if bad.MatchString(line) {
				t.Errorf("%s:%d writes a link the mount path should build (links.go): %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
