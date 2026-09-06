package ui

import (
	"path/filepath"
	"sort"
	"strings"
)

// sourceUnit reads a source file together with the files it was split into:
// name.go plus every name_*.go beside it, tests excluded, in name order. The
// structural tests here scan source text (components.go became components.go
// and thirteen components_*.go); the fields and cards they check did not move
// out of the unit, only out of the file. Reads through osReadFile so the
// existing seam still applies.
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
		raw, err := osReadFile(n)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
