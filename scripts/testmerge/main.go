// testmerge folds topic-named _test.go files into the test file of the source
// file they exercise, so a package's test count tracks its source count instead
// of its fix history.
//
//	go run ./scripts/testmerge core plan            # print the groups, change nothing
//	go run ./scripts/testmerge core apply           # merge, cap 1500 lines per result
//	go run ./scripts/testmerge apps/orchestrate apply 2000
//
// Mapping, in order: a test whose stem IS a source file stays put; otherwise the
// source file that declares the most identifiers the test references wins, if
// it wins clearly (5+ references and double the runner-up, or 8+ with a lead of
// 3) and is not a hub file every test scores against; otherwise the longest
// prefix of the test's name that is a source stem. Anything still unmapped is
// left alone and listed. Merged output keeps each file's comments verbatim,
// unions the imports, and is gofmt'd; a group whose result would pass the cap,
// mixes packages, carries a build constraint, or aliases one import two ways is
// skipped and named.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// hub files declare the shared vocabulary; every test scores against them.
// TESTMERGE_HUBS adds more, comma-separated, for a package whose type file
// would otherwise draw every test in a family onto itself.
var hub = map[string]bool{"common": true, "core": true, "llm": true, "types": true}

func init() {
	for _, h := range strings.Split(os.Getenv("TESTMERGE_HUBS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			hub[h] = true
		}
	}
}

type group struct {
	target string
	files  []string
	lines  int
}

func lines(p string) int {
	b, _ := os.ReadFile(p)
	return bytes.Count(b, []byte("\n"))
}

func main() {
	dir, mode := os.Args[1], os.Args[2]
	max := 1500
	if len(os.Args) > 3 {
		max, _ = strconv.Atoi(os.Args[3])
	}
	ents, _ := os.ReadDir(dir)
	src := map[string]bool{}
	var tests []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") {
			continue
		}
		if strings.HasSuffix(n, "_test.go") {
			tests = append(tests, n)
		} else {
			src[strings.TrimSuffix(n, ".go")] = true
		}
	}
	decl := declaredNames(dir, src)
	groups := map[string]*group{}
	var unmapped []string
	why := map[string]string{}
	for _, t := range tests {
		stem := strings.TrimSuffix(t, "_test.go")
		target := ""
		if src[stem] {
			target = stem
		} else if best, score, runner := bestSource(filepath.Join(dir, t), decl); !hub[best] && ((score >= 5 && score >= 2*runner) || (score >= 8 && score-runner >= 3)) {
			target = best
			why[t] = fmt.Sprintf("%d refs (next %d)", score, runner)
		}
		s := stem
		for target == "" {
			i := strings.LastIndex(s, "_")
			if i < 0 {
				break
			}
			s = s[:i]
			if src[s] {
				target = s
				why[t] = "prefix"
			}
		}
		if target == "" {
			unmapped = append(unmapped, t)
			continue
		}
		g := groups[target]
		if g == nil {
			g = &group{target: target}
			groups[target] = g
		}
		g.files = append(g.files, t)
		g.lines += lines(filepath.Join(dir, t))
	}
	var gs []*group
	for _, g := range groups {
		if len(g.files) > 1 {
			sort.Strings(g.files)
			gs = append(gs, g)
		}
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].lines > gs[j].lines })
	merged, removed := 0, 0
	for _, g := range gs {
		tag := ""
		if g.lines > max {
			tag = "  SKIP >max"
		}
		if mode == "plan" {
			fmt.Printf("%-32s %5d lines  %2d files%s\n", g.target+"_test.go", g.lines, len(g.files), tag)
			for _, f := range g.files {
				fmt.Printf("    %-48s %5d  %s\n", f, lines(filepath.Join(dir, f)), why[f])
			}
			continue
		}
		if g.lines > max {
			continue
		}
		if err := apply(dir, g); err != nil {
			fmt.Printf("SKIP %s: %v\n", g.target, err)
			continue
		}
		merged++
		removed += len(g.files) - 1
	}
	if mode == "plan" {
		fmt.Printf("\n%d groups; %d test files unmapped: %s\n", len(gs), len(unmapped), strings.Join(unmapped, " "))
		return
	}
	fmt.Printf("merged %d groups, removed %d files\n", merged, removed)
}

func apply(dir string, g *group) error {
	fset := token.NewFileSet()
	pkg := ""
	imports := map[string]string{} // path -> alias
	var bodies []string
	for _, f := range g.files {
		p := filepath.Join(dir, f)
		srcb, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		af, err := parser.ParseFile(fset, p, srcb, parser.ParseComments)
		if err != nil {
			return err
		}
		if pkg == "" {
			pkg = af.Name.Name
		} else if pkg != af.Name.Name {
			return fmt.Errorf("package mismatch %s vs %s in %s", pkg, af.Name.Name, f)
		}
		if bytes.Contains(srcb, []byte("//go:build")) || bytes.Contains(srcb, []byte("// +build")) {
			return fmt.Errorf("build constraint in %s", f)
		}
		for _, im := range af.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			alias := ""
			if im.Name != nil {
				alias = im.Name.Name
			}
			if prev, ok := imports[path]; ok && prev != alias {
				return fmt.Errorf("import %s aliased differently (%q vs %q) in %s", path, prev, alias, f)
			}
			imports[path] = alias
		}
		// leading comment before the package clause (file doc), if any
		lead := ""
		if af.Doc != nil {
			lead = string(srcb[fset.Position(af.Doc.Pos()).Offset:fset.Position(af.Doc.End()).Offset]) + "\n"
		}
		// body = everything after the package clause, minus import decls
		start := fset.Position(af.Name.End()).Offset
		body := srcb[start:]
		// cut import decls (from the end so offsets stay valid)
		var cuts [][2]int
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.IMPORT {
				continue
			}
			s := fset.Position(gd.Pos()).Offset - start
			e := fset.Position(gd.End()).Offset - start
			if gd.Doc != nil {
				s = fset.Position(gd.Doc.Pos()).Offset - start
			}
			cuts = append(cuts, [2]int{s, e})
		}
		for i := len(cuts) - 1; i >= 0; i-- {
			body = append(body[:cuts[i][0]:cuts[i][0]], body[cuts[i][1]:]...)
		}
		bodies = append(bodies, lead+strings.TrimSpace(string(body)))
	}
	var paths []string
	for p := range imports {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out bytes.Buffer
	fmt.Fprintf(&out, "package %s\n\n", pkg)
	if len(paths) > 0 {
		out.WriteString("import (\n")
		for _, p := range paths {
			if a := imports[p]; a != "" {
				fmt.Fprintf(&out, "\t%s %q\n", a, p)
			} else {
				fmt.Fprintf(&out, "\t%q\n", p)
			}
		}
		out.WriteString(")\n\n")
	}
	out.WriteString(strings.Join(bodies, "\n\n"))
	out.WriteString("\n")
	fmtd, err := format.Source(out.Bytes())
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	target := filepath.Join(dir, g.target+"_test.go")
	for _, f := range g.files {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	return os.WriteFile(target, fmtd, 0o644)
}

// declaredNames: source stem -> set of top-level names it declares (funcs,
// methods by name, types, vars, consts).
func declaredNames(dir string, src map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for stem := range src {
		af, err := parser.ParseFile(fset, filepath.Join(dir, stem+".go"), nil, 0)
		if err != nil {
			continue
		}
		names := map[string]bool{}
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				names[d.Name.Name] = true
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						names[sp.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
		out[stem] = names
	}
	return out
}

// bestSource scores each source file by how many distinct identifiers the test
// file references that the source declares. Common names declared in several
// files count for each; that is fine, the winner is the file with the most.
func bestSource(testPath string, decl map[string]map[string]bool) (best string, score, runner int) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, testPath, nil, 0)
	if err != nil {
		return "", 0, 0
	}
	used := map[string]bool{}
	ast.Inspect(af, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			used[n.Name] = true
		case *ast.SelectorExpr:
			used[n.Sel.Name] = true
		}
		return true
	})
	// don't let a test's own declarations vote
	for _, d := range af.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			delete(used, fd.Name.Name)
		}
	}
	type sc struct {
		stem string
		n    int
	}
	var scores []sc
	for stem, names := range decl {
		n := 0
		for u := range used {
			if names[u] {
				n++
			}
		}
		scores = append(scores, sc{stem, n})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].n != scores[j].n {
			return scores[i].n > scores[j].n
		}
		return scores[i].stem < scores[j].stem
	})
	if len(scores) == 0 {
		return "", 0, 0
	}
	if len(scores) == 1 {
		return scores[0].stem, scores[0].n, 0
	}
	return scores[0].stem, scores[0].n, scores[1].n
}
