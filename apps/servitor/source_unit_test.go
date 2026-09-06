package servitor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sourceUnit reads a source file together with the files it was split into:
// name.go plus every name_*.go beside it, tests excluded, in name order.
func sourceUnit(name string) (string, error) {
	stem := strings.TrimSuffix(name, ".go")
	names, err := filepath.Glob(stem + "_*.go")
	if err != nil {
		return "", err
	}
	names = append(names, name)
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(n)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// webSource is what web.go was before it was cut into one file per concern:
// the routes and handlers (web.go, web_*.go), the investigation session and
// the map session, the appliance record and its prompts, the terminal, the
// SSH pool and the snapshot. The structural tests below sweep this text for
// wiring that has no runtime seam, and that wiring did not move out of the
// unit, only out of the file.
func webSource(t *testing.T) string {
	t.Helper()
	body, err := sourceUnit("web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"probe_session.go", "map_session.go", "appliance.go", "appliance_prompts.go", "terminal.go", "ssh_pool.go", "snapshot.go"} {
		raw, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		body += string(raw) + "\n"
	}
	return body
}
