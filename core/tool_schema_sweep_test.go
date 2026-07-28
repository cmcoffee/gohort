package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Source-level sweep for enum values that are empty at the literal.
//
// The typed check next door only sees tools registered in THIS test binary,
// and the tool that took the whole Gemini request down lived in a package
// core does not import. One bad enum anywhere disables every tool for the
// turn, so the guard has to cover the repo, not the import graph.
//
// Crude on purpose: it matches the declaration form the codebase actually
// uses, and a false positive is a one-line fix.
func TestNoEmptyEnumValuesInSource(t *testing.T) {
	root := ".."
	enumDecl := regexp.MustCompile(`Enum:\s*\[\]string\{([^}]*)\}`)
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range enumDecl.FindAllStringSubmatch(string(src), -1) {
			for _, raw := range strings.Split(m[1], ",") {
				v := strings.TrimSpace(raw)
				if v == `""` {
					offenders = append(offenders, path+": "+strings.TrimSpace(m[0]))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("empty enum value(s) found — Gemini rejects the ENTIRE request, disabling every tool for that turn.\n"+
			"Drop the empty value and tell the caller to OMIT the param for the default:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
