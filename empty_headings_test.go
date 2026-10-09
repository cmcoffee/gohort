package main

// No form anywhere has a heading with nothing under it.
//
// The agent editor kept a Delegation heading after its controls moved to the
// Security page, and its menu offered a section that opened onto nothing.
// core/ui now drops such a heading as the form is served, so nobody sees it;
// this names any that is still written, in any app, so the leftover is
// removed where it lives instead of being quietly hidden forever.
//
// Read from the source rather than from built pages, because pages are built
// per request from records no test has. Fields are read in the order they are
// written: a heading literal followed by another heading literal is empty. A
// field list assembled by a helper with no literal in between would read as
// empty too; the exceptions below say why each one is not.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var formFieldLitRE = regexp.MustCompile(`(?:ui\.)?FormField\{([^{}\n]*)`)
var headingLabelRE = regexp.MustCompile(`Label:\s*"([^"]*)"`)

// helperCallRE is a function call opening a line: a helper that returns
// fields, in a field list.
var helperCallRE = regexp.MustCompile(`(?m)^\s*[A-Za-z_][A-Za-z0-9_.]*\(`)

func TestNoFormHeadingIsEmpty(t *testing.T) {
	roots := []string{"apps", "core"}
	if st, err := os.Stat("private"); err == nil && st.IsDir() {
		roots = append(roots, "private/")
	}
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(src)
			prev, prevEnd := "", 0
			for _, ix := range formFieldLitRE.FindAllStringSubmatchIndex(text, -1) {
				m := []string{text[ix[0]:ix[1]], text[ix[2]:ix[3]]}
				isHeading := strings.Contains(m[1], `Type: "header"`)
				// A helper that builds the section's fields, called on a line
				// of its own between the two (stageOutputRows(def, s)), is
				// fields this scan cannot see.
				if prev != "" && helperCallRE.MatchString(text[prevEnd:ix[0]]) {
					prev = ""
				}
				prevEnd = ix[1]
				if isHeading && prev != "" {
					label := ""
					if l := headingLabelRE.FindStringSubmatch(m[1]); l != nil {
						label = l[1]
					}
					t.Errorf("%s: the %q heading has no field under it before %q", path, prev, label)
				}
				prev = ""
				if isHeading {
					prev = "(heading)"
					if l := headingLabelRE.FindStringSubmatch(m[1]); l != nil {
						prev = l[1]
					}
				}
			}
			return nil
		})
	}
}
