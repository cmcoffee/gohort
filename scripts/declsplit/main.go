// declsplit moves top-level declarations out of one Go file into others in the
// same package, by the line the declaration starts on.
//
//	go run ./scripts/declsplit apps/orchestrate/runner.go runner.spec
//
// The spec is lines of `target.go: 200-342, 4722-5663`. A declaration goes to
// the target whose range holds its first line (the func/type/var/const line,
// not its doc comment); its doc comment and any free comments between it and
// the previous declaration travel with it. A target that exists gets the
// declarations appended and any imports it lacks added; a new target takes the
// source file's import block. Unused imports are left for the caller to prune
// (go build names them). Declarations in no range stay where they are.
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

type rng struct{ lo, hi int }

func main() {
	srcPath, specPath := os.Args[1], os.Args[2]
	src, err := os.ReadFile(srcPath)
	if err != nil {
		panic(err)
	}
	specB, err := os.ReadFile(specPath)
	if err != nil {
		panic(err)
	}
	targets := map[string][]rng{}
	var order []string
	for _, line := range strings.Split(string(specB), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			panic("bad spec line: " + line)
		}
		name = strings.TrimSpace(name)
		if _, seen := targets[name]; !seen {
			order = append(order, name)
		}
		for _, r := range strings.Split(rest, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			lo, hi, ok := strings.Cut(r, "-")
			if !ok {
				hi = lo
			}
			l, _ := strconv.Atoi(strings.TrimSpace(lo))
			h, _ := strconv.Atoi(strings.TrimSpace(hi))
			targets[name] = append(targets[name], rng{l, h})
		}
	}

	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, srcPath, src, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	pkg := af.Name.Name

	var importDecl *ast.GenDecl
	srcImports := map[string]string{} // path -> alias
	var srcImportOrder []string
	for _, d := range af.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			importDecl = gd
			for _, sp := range gd.Specs {
				im := sp.(*ast.ImportSpec)
				p, _ := strconv.Unquote(im.Path.Value)
				a := ""
				if im.Name != nil {
					a = im.Name.Name
				}
				srcImports[p] = a
				srcImportOrder = append(srcImportOrder, p)
			}
		}
	}
	importBlock := ""
	if importDecl != nil {
		importBlock = string(src[off(importDecl.Pos()):off(importDecl.End())])
	}

	type piece struct {
		start, end int
		target     string
	}
	var pieces []piece
	prevEnd := 0
	if importDecl != nil {
		prevEnd = off(importDecl.End())
	} else {
		prevEnd = off(af.Name.End())
	}
	moved := map[string]int{}
	for _, d := range af.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		start, end := off(d.Pos()), off(d.End())
		declLine := fset.Position(d.Pos()).Line
		// doc comment and free comments since the previous declaration
		for _, cg := range af.Comments {
			if off(cg.Pos()) >= prevEnd && off(cg.End()) <= start && off(cg.Pos()) < start {
				start = off(cg.Pos())
			}
		}
		target := ""
		for name, rs := range targets {
			for _, r := range rs {
				if declLine >= r.lo && declLine <= r.hi {
					target = name
				}
			}
		}
		if target != "" {
			// swallow the trailing newline so the residual does not collect blanks
			for end < len(src) && src[end] == '\n' {
				end++
			}
			pieces = append(pieces, piece{start, end, target})
			moved[target]++
		}
		prevEnd = off(d.End())
	}

	dir := filepath.Dir(srcPath)
	write := func(name string, b []byte) {
		out, err := format.Source(b)
		if err != nil {
			os.WriteFile(filepath.Join(dir, name+".broken"), b, 0o644)
			panic(fmt.Sprintf("%s: %v", name, err))
		}
		if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
			panic(err)
		}
	}
	for _, name := range order {
		var body bytes.Buffer
		for _, p := range pieces {
			if p.target == name {
				body.Write(src[p.start:p.end])
				body.WriteString("\n")
			}
		}
		if body.Len() == 0 {
			fmt.Printf("%s: nothing matched\n", name)
			continue
		}
		path := filepath.Join(dir, name)
		if existing, err := os.ReadFile(path); err == nil {
			// append, adding imports the target lacks
			tf, err := parser.ParseFile(fset, path, existing, parser.ParseComments)
			if err != nil {
				panic(err)
			}
			have := map[string]bool{}
			var tImp *ast.GenDecl
			for _, d := range tf.Decls {
				if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
					tImp = gd
					for _, sp := range gd.Specs {
						p, _ := strconv.Unquote(sp.(*ast.ImportSpec).Path.Value)
						have[p] = true
					}
				}
			}
			var add bytes.Buffer
			for _, p := range srcImportOrder {
				if have[p] {
					continue
				}
				if a := srcImports[p]; a != "" {
					fmt.Fprintf(&add, "\t%s %q\n", a, p)
				} else {
					fmt.Fprintf(&add, "\t%q\n", p)
				}
			}
			var out bytes.Buffer
			switch {
			case tImp == nil:
				out.Write(existing[:fset.Position(tf.Name.End()).Offset])
				out.WriteString("\n\nimport (\n")
				out.Write(add.Bytes())
				out.WriteString(")\n")
				out.Write(existing[fset.Position(tf.Name.End()).Offset:])
			case tImp.Lparen.IsValid():
				at := fset.Position(tImp.Rparen).Offset
				out.Write(existing[:at])
				out.Write(add.Bytes())
				out.Write(existing[at:])
			default:
				// single import "x" -> block
				s, e := fset.Position(tImp.Pos()).Offset, fset.Position(tImp.End()).Offset
				out.Write(existing[:s])
				out.WriteString("import (\n\t")
				out.Write(existing[fset.Position(tImp.Specs[0].Pos()).Offset:e])
				out.WriteString("\n")
				out.Write(add.Bytes())
				out.WriteString(")")
				out.Write(existing[e:])
			}
			out.WriteString("\n")
			out.Write(body.Bytes())
			write(name, out.Bytes())
			fmt.Printf("%s: appended %d declarations\n", name, moved[name])
			continue
		}
		var out bytes.Buffer
		fmt.Fprintf(&out, "package %s\n\n%s\n\n", pkg, importBlock)
		out.Write(body.Bytes())
		write(name, out.Bytes())
		fmt.Printf("%s: %d declarations\n", name, moved[name])
	}
	// residual
	sort.Slice(pieces, func(i, j int) bool { return pieces[i].start < pieces[j].start })
	var out bytes.Buffer
	pos := 0
	for _, p := range pieces {
		out.Write(src[pos:p.start])
		pos = p.end
	}
	out.Write(src[pos:])
	write(filepath.Base(srcPath), out.Bytes())
	total := 0
	for _, n := range moved {
		total += n
	}
	fmt.Printf("%s: moved %d declarations out\n", filepath.Base(srcPath), total)
}
