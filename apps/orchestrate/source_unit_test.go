package orchestrate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sourceUnit reads a source file together with the files it was split into:
// name.go plus every name_*.go beside it, tests excluded, in name order. The
// structural tests in this package scan source text for wiring that has no
// runtime seam; when a file is cut into a family (runner.go became runner.go
// and fifteen runner_*.go), the wiring they assert did not move out of the
// unit, only out of the file, so this is what "the runner source" means.
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
