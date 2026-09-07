// loopsplit cuts one large function whose bulk is a single round loop into
// methods on a state struct, with go/types resolving every identifier so only
// the function's own locals are rewritten (a shadowed `err` in an if-init is
// left alone) and the struct fields come out with their exact types.
//
//	go run ./scripts/loopsplit core runAgentLoopInner agent_loop.spec /tmp/generated.go
//
// Run from the directory the package builds in (go/types needs the imports);
// write the output OUTSIDE the package and assemble by hand.
//
// Spec lines:
//
//	recv lr *loopRun                 receiver name and type
//	round rs roundState              per-round field name and its struct type
//	action loopAction                the action enum type (actNone/actContinue/actBreak/actReturn)
//	result loopResult                the struct that carries an early return (fields resp, history, err)
//	roundvar round                   the loop variable, exposed as a field of the receiver
//	method <name>(<params>) <results> : <line ranges>     function-level statements, verbatim
//	loopmethod <name>() : <line ranges>                   loop-body statements; continue/break/return translated
//
// Function-level locals become fields of the receiver; loop-body locals become
// fields of the per-round struct (zeroed by the driver each round, exactly like
// the declarations they replace). A `:=` on those becomes `=`; a bare `var x T`
// is dropped; `const` and `type` declarations are hoisted to package level.
// A closure `name := func(...) {...}` at either level becomes a method.
// Declarations of function-level locals keep their leading comment on the
// struct field. Everything else, comments included, moves byte for byte.
//
// Output: <out.go> with the hoisted declarations, both structs, the action
// helpers, and every method. The driver function is written by hand.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strconv"
	"strings"
)

type rng struct{ lo, hi int }

type method struct {
	name, sig string
	ranges    []rng
	loop      bool
}

type decl struct {
	obj     types.Object
	name    string
	typ     string
	comment string // leading comment of a dropped `var x T`
	closure *ast.FuncLit
	loop    bool
	order   int
}

func main() {
	dir, fnName, specPath, outPath := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	specB, err := os.ReadFile(specPath)
	if err != nil {
		panic(err)
	}
	var recvName, recvType, roundField, roundType, actionType, resultType, roundVar string
	var methods []*method
	for _, line := range strings.Split(string(specB), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "recv":
			recvName, recvType = f[1], strings.Join(f[2:], " ")
		case "round":
			roundField, roundType = f[1], f[2]
		case "action":
			actionType = f[1]
		case "result":
			resultType = f[1]
		case "roundvar":
			roundVar = f[1]
		case "method", "loopmethod":
			rest := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
			head, tail, ok := strings.Cut(rest, ":")
			if !ok {
				panic("bad method line: " + line)
			}
			m := &method{loop: f[0] == "loopmethod"}
			head = strings.TrimSpace(head)
			m.name = head[:strings.Index(head, "(")]
			m.sig = head[strings.Index(head, "("):]
			if m.loop {
				m.sig = "() " + actionType
			}
			for _, r := range strings.Fields(tail) {
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

	// ---- parse + type-check the package ----
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	var files []*ast.File
	var pkgName string
	for name, p := range pkgs {
		pkgName = name
		var names []string
		for n := range p.Files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			files = append(files, p.Files[n])
		}
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil), Error: func(error) {}}
	pkg, _ := conf.Check(pkgName, fset, files, info)
	qual := types.RelativeTo(pkg)

	var fn *ast.FuncDecl
	var file *ast.File
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fnName {
				fn, file = fd, f
			}
		}
	}
	if fn == nil {
		panic("no func " + fnName)
	}
	src, err := os.ReadFile(fset.Position(file.Pos()).Filename)
	if err != nil {
		panic(err)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	// stmtEnd is the byte after a statement's line: its trailing same-line
	// comment and newline travel with it, plus any blank lines that follow.
	stmtEnd := func(s ast.Node) int {
		j := off(s.End())
		for j < len(src) && src[j] != '\n' {
			if src[j] == ' ' || src[j] == '	' {
				j++
				continue
			}
			if j+1 < len(src) && src[j] == '/' && src[j+1] == '/' {
				for j < len(src) && src[j] != '\n' {
					j++
				}
				break
			}
			return off(s.End()) // something else on the line: leave it
		}
		for j < len(src) && src[j] == '\n' {
			j++
		}
		return j
	}

	var loop *ast.ForStmt
	for _, s := range fn.Body.List {
		if fs, ok := s.(*ast.ForStmt); ok {
			loop = fs
			break
		}
	}
	if loop == nil {
		panic("no top-level for loop")
	}

	// ---- collect declarations ----
	byObj := map[types.Object]*decl{}
	var decls []*decl
	var hoisted []string // const/type declarations, verbatim with comments
	order := 0
	leadingComment := func(start int, node ast.Node) (int, string) {
		// comments between the previous statement end and this node
		s := off(node.Pos())
		for _, cg := range file.Comments {
			if off(cg.Pos()) >= start && off(cg.End()) <= s && off(cg.Pos()) < s {
				s = off(cg.Pos())
			}
		}
		return s, strings.TrimSpace(string(src[s:off(node.Pos())]))
	}
	add := func(id *ast.Ident, loopLevel bool, comment string, fl *ast.FuncLit) {
		o := info.Defs[id]
		if o == nil || id.Name == "_" {
			return
		}
		d := &decl{obj: o, name: id.Name, typ: types.TypeString(o.Type(), qual), comment: comment, closure: fl, loop: loopLevel, order: order}
		order++
		byObj[o] = d
		decls = append(decls, d)
	}
	// receiver + params
	if fn.Recv != nil {
		for _, f := range fn.Recv.List {
			for _, n := range f.Names {
				add(n, false, "", nil)
			}
		}
	}
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			add(n, false, "", nil)
		}
	}
	collect := func(list []ast.Stmt, loopLevel bool, blockStart int) {
		prev := blockStart
		for _, s := range list {
			_, cmt := leadingComment(prev, s)
			switch st := s.(type) {
			case *ast.AssignStmt:
				if st.Tok == token.DEFINE {
					var fl *ast.FuncLit
					if len(st.Rhs) == 1 {
						fl, _ = st.Rhs[0].(*ast.FuncLit)
					}
					for _, l := range st.Lhs {
						if id, ok := l.(*ast.Ident); ok {
							add(id, loopLevel, "", fl)
						}
					}
				}
			case *ast.DeclStmt:
				gd := st.Decl.(*ast.GenDecl)
				switch gd.Tok {
				case token.VAR:
					for _, sp := range gd.Specs {
						vs := sp.(*ast.ValueSpec)
						c := ""
						if len(vs.Values) == 0 {
							c = cmt
							if len(gd.Specs) > 1 {
								c = ""
							}
						}
						for _, n := range vs.Names {
							add(n, loopLevel, c, nil)
						}
					}
				case token.CONST, token.TYPE:
					s0, _ := leadingComment(prev, s)
					hoisted = append(hoisted, string(src[s0:off(s.End())]))
				}
			}
			prev = stmtEnd(s)
		}
	}
	collect(fn.Body.List, false, off(fn.Body.Lbrace)+1)
	collect(loop.Body.List, true, off(loop.Body.Lbrace)+1)
	// the loop variable
	if roundVar != "" {
		if as, ok := loop.Init.(*ast.AssignStmt); ok {
			for _, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == roundVar {
					add(id, false, "", nil)
				}
			}
		}
	}

	// ---- rewriting ----
	type edit struct {
		s, e int
		text string
	}
	prefixFor := func(o types.Object) (string, bool) {
		d := byObj[o]
		if d == nil {
			return "", false
		}
		if d.closure != nil {
			return recvName + ".", true
		}
		if d.loop {
			return recvName + "." + roundField + ".", true
		}
		return recvName + ".", true
	}
	// identEdits returns prefix insertions for every resolved identifier in
	// node, skipping nothing: closures keep their captures by field.
	identEdits := func(node ast.Node) []edit {
		var eds []edit
		ast.Inspect(node, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			o := info.Uses[id]
			if o == nil {
				o = info.Defs[id]
			}
			if o == nil {
				return true
			}
			if p, ok := prefixFor(o); ok {
				eds = append(eds, edit{off(id.Pos()), off(id.Pos()), p})
			}
			return true
		})
		return eds
	}
	apply := func(text []byte, base int, eds []edit) string {
		// an insertion inside a replaced span is already part of the replacement
		var kept []edit
		for _, e := range eds {
			inside := false
			if e.s == e.e {
				for _, r := range eds {
					if r.s < r.e && r.s <= e.s && e.s < r.e {
						inside = true
					}
				}
			}
			if !inside {
				kept = append(kept, e)
			}
		}
		eds = kept
		sort.Slice(eds, func(i, j int) bool {
			if eds[i].s != eds[j].s {
				return eds[i].s > eds[j].s
			}
			return eds[i].e > eds[j].e
		})
		out := append([]byte{}, text...)
		for _, e := range eds {
			s, en := e.s-base, e.e-base
			out = append(out[:s], append([]byte(e.text), out[en:]...)...)
		}
		return string(out)
	}
	// controlEdits translates loop-targeting branches and returns inside one
	// loop-body statement. Returns replacements that already include the
	// identifier prefixes of their operands.
	controlEdits := func(node ast.Node, idents []edit) []edit {
		var eds []edit
		type frame struct{ loops, switches int }
		var walk func(n ast.Node, fr frame)
		walk = func(n ast.Node, fr frame) {
			if n == nil {
				return
			}
			switch x := n.(type) {
			case *ast.FuncLit:
				return // returns/branches inside belong to the closure
			case *ast.ForStmt, *ast.RangeStmt:
				fr.loops++
			case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				fr.switches++
			case *ast.BranchStmt:
				if x.Label != nil {
					panic("labeled branch at " + fset.Position(x.Pos()).String())
				}
				switch x.Tok {
				case token.CONTINUE:
					if fr.loops == 0 {
						eds = append(eds, edit{off(x.Pos()), off(x.End()), "return actContinue"})
					}
				case token.BREAK:
					if fr.loops == 0 && fr.switches == 0 {
						eds = append(eds, edit{off(x.Pos()), off(x.End()), "return actBreak"})
					}
				case token.GOTO:
					panic("goto at " + fset.Position(x.Pos()).String())
				}
				return
			case *ast.ReturnStmt:
				var parts []string
				for _, r := range x.Results {
					s, e := off(r.Pos()), off(r.End())
					var sub []edit
					for _, ie := range idents {
						if ie.s >= s && ie.s <= e {
							sub = append(sub, ie)
						}
					}
					parts = append(parts, apply(src[s:e], s, sub))
				}
				eds = append(eds, edit{off(x.Pos()), off(x.End()), "return " + recvName + ".exit(" + strings.Join(parts, ", ") + ")"})
				return
			}
			// recurse into children with the updated frame
			ast.Inspect(n, func(c ast.Node) bool {
				if c == n {
					return true
				}
				walk(c, fr)
				return false
			})
		}
		walk(node, frame{})
		return eds
	}

	var out bytes.Buffer
	out.WriteString("package " + pkgName + "\n\n")
	// imports: copied from the source file, pruned by the caller's build loop
	for _, d := range file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			out.Write(src[off(gd.Pos()):off(gd.End())])
			out.WriteString("\n\n")
			break
		}
	}
	for _, h := range hoisted {
		out.WriteString(h + "\n\n")
	}
	// structs
	writeStruct := func(name string, loopLevel bool) {
		out.WriteString("type " + name + " struct {\n")
		for _, d := range decls {
			if d.loop != loopLevel || d.closure != nil {
				continue
			}
			if d.comment != "" {
				for _, l := range strings.Split(d.comment, "\n") {
					out.WriteString("\t" + strings.TrimSpace(l) + "\n")
				}
			}
			out.WriteString("\t" + d.name + " " + d.typ + "\n")
		}
		if !loopLevel {
			out.WriteString("\n\t// " + roundField + " is the current round's state, zeroed by the driver at the top of\n\t// every round exactly as the declarations it replaces were.\n\t" + roundField + " " + roundType + "\n")
			out.WriteString("\t// ret carries an early return out of a round method (see exit).\n\tret " + resultType + "\n")
		}
		out.WriteString("}\n\n")
	}
	writeStruct(strings.TrimPrefix(recvType, "*"), false)
	writeStruct(roundType, true)
	out.WriteString("// " + actionType + " is what a round method tells the driver to do next.\ntype " + actionType + " int\n\nconst (\n\tactNone " + actionType + " = iota // carry on with the next phase of this round\n\tactContinue                        // next round\n\tactBreak                           // leave the loop and finish\n\tactReturn                          // return " + recvName + ".ret from the function\n)\n\n")
	out.WriteString("// " + resultType + " is the function's return, parked by exit until the driver returns it.\ntype " + resultType + " struct {\n\tresp    *Response\n\thistory []Message\n\terr     error\n}\n\n")
	out.WriteString("// exit records an early return and tells the driver to take it.\nfunc (" + recvName + " " + recvType + ") exit(resp *Response, history []Message, err error) " + actionType + " {\n\t" + recvName + ".ret = " + resultType + "{resp, history, err}\n\treturn actReturn\n}\n")

	// statement index for both levels
	type stmt struct {
		node       ast.Stmt
		start, end int
		line       int
		loop       bool
	}
	var stmts []stmt
	index := func(list []ast.Stmt, loopLevel bool, blockStart int) {
		prev := blockStart
		for _, s := range list {
			st := stmt{node: s, end: stmtEnd(s), line: fset.Position(s.Pos()).Line, loop: loopLevel}
			st.start, _ = leadingComment(prev, s)
			stmts = append(stmts, st)
			prev = st.end
		}
	}
	index(fn.Body.List, false, off(fn.Body.Lbrace)+1)
	index(loop.Body.List, true, off(loop.Body.Lbrace)+1)

	emitMethod := func(name, sig, body string) {
		out.WriteString("\nfunc (" + recvName + " " + recvType + ") " + name + sig + " {\n" + body + "\n}\n")
	}
	// closures become methods wherever they are declared
	emitClosure := func(d *decl) {
		fl := d.closure
		sig := string(src[off(fl.Type.Pos())+len("func") : off(fl.Type.End())])
		inner := src[off(fl.Body.Lbrace)+1 : off(fl.Body.Rbrace)]
		eds := identEdits(fl.Body)
		body := strings.TrimRight(strings.TrimLeft(apply(inner, off(fl.Body.Lbrace)+1, eds), "\n"), "\n\t ")
		emitMethod(d.name, sig, body)
	}
	used := map[int]bool{}
	for _, m := range methods {
		var body bytes.Buffer
		for i, st := range stmts {
			if st.loop != m.loop {
				continue
			}
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
			base := st.start
			switch n := st.node.(type) {
			case *ast.AssignStmt:
				if n.Tok == token.DEFINE && len(n.Lhs) == 1 && len(n.Rhs) == 1 {
					if id, ok := n.Lhs[0].(*ast.Ident); ok {
						if d := byObj[info.Defs[id]]; d != nil && d.closure != nil {
							lead := strings.TrimSpace(string(src[st.start:off(n.Pos())]))
							if lead != "" {
								out.WriteString("\n" + lead)
							}
							emitClosure(d)
							continue
						}
					}
				}
				if n.Tok == token.DEFINE {
					allShared := true
					for _, l := range n.Lhs {
						id, ok := l.(*ast.Ident)
						if !ok || (id.Name != "_" && byObj[info.Defs[id]] == nil) {
							allShared = false
						}
					}
					if allShared {
						eds := identEdits(n)
						if m.loop {
							eds = append(eds, controlEdits(n, identEdits(n))...)
						}
						eds = append(eds, edit{off(n.TokPos), off(n.TokPos) + 2, "="})
						body.WriteString(apply(text, base, eds))
						continue
					}
				}
			case *ast.DeclStmt:
				gd := n.Decl.(*ast.GenDecl)
				switch gd.Tok {
				case token.CONST, token.TYPE:
					continue // hoisted
				case token.VAR:
					// var x T -> dropped (field); var x = v -> assignment
					var lines []string
					for _, sp := range gd.Specs {
						vs := sp.(*ast.ValueSpec)
						if len(vs.Values) == 0 {
							continue
						}
						var lhs []string
						for _, nm := range vs.Names {
							p, _ := prefixFor(info.Defs[nm])
							lhs = append(lhs, p+nm.Name)
						}
						var rhs []string
						for _, v := range vs.Values {
							rhs = append(rhs, apply(src[off(v.Pos()):off(v.End())], off(v.Pos()), identEdits(v)))
						}
						lines = append(lines, "\t"+strings.Join(lhs, ", ")+" = "+strings.Join(rhs, ", "))
					}
					if len(lines) > 0 {
						lead := strings.TrimSpace(string(src[st.start:off(n.Pos())]))
						if lead != "" {
							body.WriteString("\t" + lead + "\n")
						}
						body.WriteString(strings.Join(lines, "\n") + "\n\n")
					}
					continue
				}
			}
			eds := identEdits(st.node)
			if m.loop {
				eds = append(eds, controlEdits(st.node, identEdits(st.node))...)
			}
			body.WriteString(apply(text, base, eds))
		}
		b := strings.TrimRight(body.String(), "\n\t ")
		if m.loop {
			b += "\n\treturn actNone"
		}
		emitMethod(m.name, m.sig, b)
	}
	// closures never reached through a method range (declared inside a range
	// that was skipped) are still emitted so the spec cannot lose one.
	for i, st := range stmts {
		if used[i] {
			continue
		}
		if as, ok := st.node.(*ast.AssignStmt); ok && as.Tok == token.DEFINE && len(as.Lhs) == 1 {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				if d := byObj[info.Defs[id]]; d != nil && d.closure != nil {
					fmt.Printf("  closure outside every range, emitted anyway: %s (L%d)\n", d.name, st.line)
					emitClosure(d)
					used[i] = true
				}
			}
		}
	}
	if err := os.WriteFile(outPath, out.Bytes(), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s: %d statements, %d used\n", fnName, len(stmts), len(used))
	for i, st := range stmts {
		if !used[i] {
			lvl := "fn"
			if st.loop {
				lvl = "loop"
			}
			fmt.Printf("  unused %s stmt L%d: %s\n", lvl, st.line, strings.SplitN(string(src[off(st.node.Pos()):st.end]), "\n", 2)[0])
		}
	}
}
