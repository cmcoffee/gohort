package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/cmcoffee/gohort"

// registersItself matches a package that adds a tool or an app from its own
// init: it does nothing until the binary imports it.
var registersItself = regexp.MustCompile(`\bRegister(ChatTool|App)\(`)

// notInTheBinary are the packages left out on purpose, and why.
var notInTheBinary = map[string]string{
	"apps/hello": "the scaffold core/ui/AUTHORING.md points new apps at: copied, not run",
}

// Every package under tools/ and apps/ that registers a tool or an app is
// imported by the binary, through tools.go, agents.go or a package they pull
// in. A package nothing imports builds and passes its own tests, and is not
// in the binary: an assistant added a tool with a table of tests, all
// passing, said the import "was not needed since init handles it", and the
// tool did not exist.
func TestEveryToolAndAppPackageIsInTheBinary(t *testing.T) {
	reached := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		for _, imp := range packageImports(t, dir) {
			if !strings.HasPrefix(imp, modulePath+"/") || reached[imp] {
				continue
			}
			reached[imp] = true
			walk(strings.TrimPrefix(imp, modulePath+"/"))
		}
	}
	walk(".")

	var missing []string
	for _, root := range []string{"tools", "apps"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			if !e.IsDir() || notInTheBinary[filepath.ToSlash(dir)] != "" || !registers(t, dir) {
				continue
			}
			if !reached[modulePath+"/"+filepath.ToSlash(dir)] {
				missing = append(missing, dir)
			}
		}
	}
	sort.Strings(missing)
	for _, dir := range missing {
		t.Errorf("%s registers a tool or an app, and nothing the binary imports reaches it: add it to tools.go (a tool) or agents.go (an app)", dir)
	}
}

// goSources are dir's Go files, tests left out.
func goSources(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	return out
}

// packageImports is what dir's sources import.
func packageImports(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	for _, f := range goSources(t, dir) {
		file, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, imp := range file.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// registers reports whether dir's sources register a tool or an app.
func registers(t *testing.T, dir string) bool {
	t.Helper()
	for _, f := range goSources(t, dir) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if registersItself.Match(src) {
			return true
		}
	}
	return false
}
