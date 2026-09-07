// funcsplit: cut one large function into methods on a state struct, moving
// its top-level statements verbatim (comments included) and prefixing the
// shared locals with the receiver. Declarations of shared locals are dropped
// (they become struct fields, written by hand); `x := ...` on shared locals
// becomes `pr.x = ...`; a `name := func(...) {...}` closure named in the spec
// becomes a method. Identifier rewriting is token-aware (go/scanner), so
// strings and comments are never touched.
//
//	go run ./scripts/funcsplit <copy-of-file.go> <spec>   # run on a COPY: it writes <file>.methods.go beside it
//
// Spec format (line-oriented):
//
//	func runPlan
//	recv pr *planRun
//	this t := pr.t          # prelude added to a method whose body uses `t`
//	shared name1 name2 ...  # locals that become fields (may repeat)
//	closure name1 name2 ... # closures that become methods (may repeat)
//	method <name>(<params>) <results> : <ranges> [return]
//	prelude <name> <go code>  # inserted at the top of that method's body
//
// Output: <file>.methods.go holding the generated methods (package clause and
// imports copied from the source), and the original function's byte range
// printed so the caller can replace it by hand.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"strconv"
	"strings"
)

type rng struct{ lo, hi int }

type method struct {
	name, sig string
	ranges    []rng
	retMode   bool
	prelude   []string
}

func main() {
	file, specPath := os.Args[1], os.Args[2]
	src, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}
	specB, err := os.ReadFile(specPath)
	if err != nil {
		panic(err)
	}
	var funcName, recvName, recvType, this string
	shared := map[string]bool{}
	closures := map[string]bool{}
	var methods []*method
	preludes := map[string][]string{}
	for _, line := range strings.Split(string(specB), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "func":
			funcName = f[1]
		case "recv":
			recvName, recvType = f[1], strings.Join(f[2:], " ")
		case "this":
			this = strings.TrimSpace(strings.TrimPrefix(line, "this"))
		case "shared":
			for _, n := range f[1:] {
				shared[n] = true
			}
		case "closure":
			for _, n := range f[1:] {
				closures[n] = true
			}
		case "prelude":
			preludes[f[1]] = append(preludes[f[1]], strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "prelude"), " "+f[1])))
		case "method":
			rest := strings.TrimSpace(strings.TrimPrefix(line, "method"))
			head, tail, ok := strings.Cut(rest, ":")
			if !ok {
				panic("bad method line: " + line)
			}
			m := &method{}
			head = strings.TrimSpace(head)
			m.name = head[:strings.Index(head, "(")]
			m.sig = head[strings.Index(head, "("):]
			for _, r := range strings.Fields(tail) {
				if r == "return" {
					m.retMode = true
					continue
				}
				r = strings.TrimSuffix(r, ",")
				lo, hi, ok := strings.Cut(r, "-")
				if !ok {
					hi = lo
				}
				l, _ := strconv.Atoi(lo)
				h, _ := strconv.Atoi(hi)
				m.ranges = append(m.ranges, rng{l, h})
			}
			methods = append(methods, m)
		}
	}
	for _, m := range methods {
		m.prelude = preludes[m.name]
	}

	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var fn *ast.FuncDecl
	for _, d := range af.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			fn = fd
		}
	}
	if fn == nil {
		panic("no func " + funcName)
	}

	type stmt struct {
		node       ast.Stmt
		start, end int // with leading comments
		line       int
	}
	var stmts []stmt
	prevEnd := off(fn.Body.Lbrace) + 1
	for _, s := range fn.Body.List {
		st := stmt{node: s, start: off(s.Pos()), end: off(s.End()), line: fset.Position(s.Pos()).Line}
		for _, cg := range af.Comments {
			if off(cg.Pos()) >= prevEnd && off(cg.End()) <= st.start && off(cg.Pos()) < st.start {
				st.start = off(cg.Pos())
			}
		}
		stmts = append(stmts, st)
		prevEnd = st.end
	}

	// rewrite rewrites one statement's source text: identifiers in shared or
	// closures get the receiver prefix unless selected through a dot;
	// `:=` whose LHS are all shared (or blank) becomes `=`.
	rewrite := func(text []byte, defineToAssign bool) string {
		var sc scanner.Scanner
		fs := token.NewFileSet()
		f := fs.AddFile("", fs.Base(), len(text))
		sc.Init(f, text, nil, scanner.ScanComments)
		var out bytes.Buffer
		last := 0
		prevTok := token.ILLEGAL
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			o := fs.Position(pos).Offset
			if tok == token.IDENT && (shared[lit] || closures[lit]) && prevTok != token.PERIOD {
				out.Write(text[last:o])
				out.WriteString(recvName + ".")
				last = o
			}
			if tok == token.DEFINE && defineToAssign {
				// only the statement's own := — nested closures keep theirs
				out.Write(text[last:o])
				out.WriteString("=")
				last = o + 2
				defineToAssign = false
			}
			if tok != token.COMMENT {
				prevTok = tok
			}
		}
		out.Write(text[last:])
		return out.String()
	}
	allSharedLHS := func(as *ast.AssignStmt) bool {
		for _, l := range as.Lhs {
			id, ok := l.(*ast.Ident)
			if !ok {
				return false
			}
			if id.Name != "_" && !shared[id.Name] {
				return false
			}
		}
		return true
	}
	declaresOnlyShared := func(ds *ast.DeclStmt) bool {
		gd, ok := ds.Decl.(*ast.GenDecl)
		if !ok {
			return false
		}
		for _, sp := range gd.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				return false
			}
			for _, n := range vs.Names {
				if !shared[n.Name] {
					return false
				}
			}
		}
		return true
	}
	usesIdent := func(text string, name string) bool {
		var sc scanner.Scanner
		fs := token.NewFileSet()
		f := fs.AddFile("", fs.Base(), len(text))
		sc.Init(f, []byte(text), nil, 0)
		prev := token.ILLEGAL
		for {
			_, tok, lit := sc.Scan()
			if tok == token.EOF {
				return false
			}
			if tok == token.IDENT && lit == name && prev != token.PERIOD {
				return true
			}
			prev = tok
		}
	}
	emitMethod := func(out *bytes.Buffer, name, sig, body string, prelude []string) {
		out.WriteString("\nfunc (" + recvName + " " + recvType + ") " + name + sig + " {\n")
		thisVar := strings.TrimSpace(strings.SplitN(this, " ", 2)[0])
		if this != "" && usesIdent(body, thisVar) {
			out.WriteString("\t" + this + "\n")
		}
		for _, p := range prelude {
			out.WriteString("\t" + p + "\n")
		}
		out.WriteString(body)
		out.WriteString("\n}\n")
	}

	var out bytes.Buffer
	out.WriteString("package " + af.Name.Name + "\n\n")
	for _, d := range af.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			out.Write(src[off(gd.Pos()):off(gd.End())])
			out.WriteString("\n")
			break
		}
	}
	used := map[int]bool{}
	for _, m := range methods {
		var body bytes.Buffer
		for i, st := range stmts {
			in := false
			for _, r := range m.ranges {
				if st.line >= r.lo && st.line <= r.hi {
					in = true
				}
			}
			if !in {
				continue
			}
			used[i] = true
			text := src[st.start:st.end]
			// closure -> its own method, not part of this body
			if as, ok := st.node.(*ast.AssignStmt); ok && as.Tok == token.DEFINE && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
				if id, ok := as.Lhs[0].(*ast.Ident); ok && closures[id.Name] {
					if fl, ok := as.Rhs[0].(*ast.FuncLit); ok {
						sig := string(src[off(fl.Type.Pos())+len("func") : off(fl.Type.End())])
						inner := src[off(fl.Body.Lbrace)+1 : off(fl.Body.Rbrace)]
						lead := src[st.start:off(as.Pos())]
						out.Write(lead)
						emitMethod(&out, id.Name, sig, strings.TrimRight(rewrite(bytes.TrimLeft(inner, "\n"), false), "\n\t "), nil)
						continue
					}
				}
			}
			if m.retMode {
				as, ok := st.node.(*ast.AssignStmt)
				if !ok || len(as.Rhs) != 1 {
					panic(m.name + ": return mode needs a single assignment")
				}
				lead := src[st.start:off(as.Pos())]
				body.Write(lead)
				body.WriteString("\treturn " + rewrite(src[off(as.Rhs[0].Pos()):off(as.Rhs[0].End())], false) + "\n")
				continue
			}
			switch n := st.node.(type) {
			case *ast.DeclStmt:
				if declaresOnlyShared(n) {
					continue // becomes a struct field
				}
			case *ast.AssignStmt:
				if n.Tok == token.DEFINE && allSharedLHS(n) {
					body.WriteString(rewrite(text, true))
					body.WriteString("\n")
					continue
				}
			}
			body.WriteString(rewrite(text, false))
			body.WriteString("\n")
		}
		emitMethod(&out, m.name, m.sig, strings.TrimRight(body.String(), "\n"), m.prelude)
	}
	if err := os.WriteFile(file+".methods.go", out.Bytes(), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s: statements %d, used %d; function spans bytes %d-%d (lines %d-%d)\n", funcName, len(stmts), len(used), off(fn.Pos()), off(fn.End()), fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line)
	for i, st := range stmts {
		if !used[i] {
			fmt.Printf("  unused stmt L%d: %s\n", st.line, strings.SplitN(string(src[off(st.node.Pos()):st.end]), "\n", 2)[0])
		}
	}
}
