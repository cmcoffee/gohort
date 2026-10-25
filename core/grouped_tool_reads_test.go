package core

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The note that names a call's unused keys is only as true as each action's
// Params and Accepts. A handler that reads a key declared in neither would be
// told it did nothing when it did, so every grouped action in the tree is
// read here and each key its handler reads by name must be declared.
//
// Parsed, not run: handlers have side effects. What it can see is a key
// named as a string literal (StringArg(args, "k"), args["k"], or a range over
// a literal list or map whose element indexes args), in the handler and in
// the same-package functions it passes args on to. A forward into another
// package is not followed, so an action that hands args to another tool
// lists that tool's extra keys in Accepts by hand (workspace write's mode).
func TestGroupedActionsDeclareWhatTheyRead(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if n := info.Name(); p != root && (strings.HasPrefix(n, ".") || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("GroupedToolAction{")) {
				dirs[filepath.Dir(p)] = true
			}
		}
		return nil
	})
	if len(dirs) < 3 {
		t.Fatalf("found grouped actions in only %d package(s) under %s: the walk is not reaching the tree", len(dirs), root)
	}
	checked := 0
	for dir := range dirs {
		s := newReadScan(t, dir)
		for _, f := range s.files {
			ast.Inspect(f, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok || !isGroupedActionType(cl.Type) {
					return true
				}
				if s.checkAction(t, root, cl) {
					checked++
				}
				return true
			})
		}
	}
	if checked < 40 {
		t.Fatalf("checked only %d grouped actions: the scan is missing most of them", checked)
	}
}

type readScan struct {
	fset    *token.FileSet
	files   []*ast.File
	funcs   map[string]*ast.FuncDecl
	methods map[string][]*ast.FuncDecl
}

func newReadScan(t *testing.T, dir string) *readScan {
	s := &readScan{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, methods: map[string][]*ast.FuncDecl{}}
	pkgs, err := parser.ParseDir(s.fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			s.files = append(s.files, f)
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fd.Recv == nil {
					s.funcs[fd.Name.Name] = fd
				} else {
					s.methods[fd.Name.Name] = append(s.methods[fd.Name.Name], fd)
				}
			}
		}
	}
	return s
}

func isGroupedActionType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "GroupedToolAction"
	case *ast.SelectorExpr:
		return x.Sel.Name == "GroupedToolAction"
	}
	return false
}

// checkAction reports each key the action's handler reads that it does not
// declare. False when the action could not be checked: no handler, or a
// Params that is not written out here.
func (s *readScan) checkAction(t *testing.T, root string, cl *ast.CompositeLit) bool {
	declared := map[string]bool{"action": true}
	var params, handler ast.Expr
	for _, e := range cl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, _ := kv.Key.(*ast.Ident)
		if id == nil {
			continue
		}
		switch id.Name {
		case "Params":
			params = kv.Value
		case "Accepts":
			if lit, ok := kv.Value.(*ast.CompositeLit); ok {
				for _, k := range stringLits(lit.Elts) {
					declared[k] = true
				}
			}
		case "Handler":
			handler = kv.Value
		}
	}
	lit, ok := params.(*ast.CompositeLit)
	if !ok || handler == nil {
		return false
	}
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			for _, k := range stringLits([]ast.Expr{kv.Key}) {
				declared[k] = true
			}
		}
	}
	reads := map[string]bool{}
	seen := map[*ast.FuncDecl]bool{}
	s.followHandler(handler, reads, seen)
	var undeclared []string
	for k := range reads {
		if !declared[k] && !strings.HasPrefix(k, "_") {
			undeclared = append(undeclared, strconv.Quote(k))
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		pos := s.fset.Position(cl.Pos())
		rel, _ := filepath.Rel(root, pos.Filename)
		t.Errorf("%s:%d: the handler reads %s, declared in neither Params nor Accepts. Add each to Params when the model should send it, or to Accepts when only the framework does (or a tool it forwards to honors it): an undeclared key a call sends is reported back as doing nothing",
			rel, pos.Line, strings.Join(undeclared, ", "))
	}
	return true
}

func (s *readScan) followHandler(h ast.Expr, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	switch x := h.(type) {
	case *ast.FuncLit:
		s.walkFunc(x.Type, x.Body, 0, reads, seen)
	case *ast.Ident, *ast.SelectorExpr:
		if fd := s.resolve(x); fd != nil && !seen[fd] {
			seen[fd] = true
			s.walkFunc(fd.Type, fd.Body, 0, reads, seen)
		}
	case *ast.CallExpr:
		// A wrapper around the real handler: connectorMadeHere(connectorCreate).
		for _, a := range x.Args {
			s.followHandler(a, reads, seen)
		}
	}
}

// resolve finds the same-package function a call names. A method is followed
// only when its name is the package's only method by that name: without types
// there is no telling which RunWithSession a call reaches.
func (s *readScan) resolve(e ast.Expr) *ast.FuncDecl {
	switch x := e.(type) {
	case *ast.Ident:
		return s.funcs[x.Name]
	case *ast.SelectorExpr:
		if ms := s.methods[x.Sel.Name]; len(ms) == 1 {
			return ms[0]
		}
	}
	return nil
}

func (s *readScan) walkFunc(ft *ast.FuncType, body *ast.BlockStmt, depth int, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	if body == nil || ft.Params == nil {
		return
	}
	for _, f := range ft.Params.List {
		if _, ok := f.Type.(*ast.MapType); ok && len(f.Names) > 0 && f.Names[0].Name != "_" {
			s.walkBody(f.Names[0].Name, body, depth, reads, seen)
			return
		}
	}
}

func (s *readScan) walkBody(argName string, body *ast.BlockStmt, depth int, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	// Loop variables ranging over a written-out list or map of names: a key
	// read through one of them is each of those names.
	bound := map[string][]string{}
	ast.Inspect(body, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		lit, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch lit.Type.(type) {
		case *ast.ArrayType:
			if v, ok := rs.Value.(*ast.Ident); ok {
				bound[v.Name] = append(bound[v.Name], stringLits(lit.Elts)...)
			}
		case *ast.MapType:
			if k, ok := rs.Key.(*ast.Ident); ok {
				var keys []ast.Expr
				for _, e := range lit.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						keys = append(keys, kv.Key)
					}
				}
				bound[k.Name] = append(bound[k.Name], stringLits(keys)...)
			}
		}
		return true
	})
	isArgs := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == argName
	}
	key := func(e ast.Expr) {
		for _, k := range stringLits([]ast.Expr{e}) {
			reads[k] = true
		}
		if id, ok := e.(*ast.Ident); ok {
			for _, k := range bound[id.Name] {
				reads[k] = true
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IndexExpr:
			if isArgs(x.X) {
				key(x.Index)
			}
		case *ast.CallExpr:
			for i, a := range x.Args {
				if !isArgs(a) {
					continue
				}
				if i+1 < len(x.Args) {
					key(x.Args[i+1])
				}
				if depth >= 6 {
					continue
				}
				fd := s.resolve(x.Fun)
				if fd == nil || seen[fd] {
					continue
				}
				seen[fd] = true
				if name := paramNameAt(fd.Type, i); name != "" && name != "_" {
					s.walkBody(name, fd.Body, depth+1, reads, seen)
				}
			}
		}
		return true
	})
}

func paramNameAt(ft *ast.FuncType, i int) string {
	idx := 0
	for _, f := range ft.Params.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		if i < idx+n {
			if len(f.Names) == 0 {
				return ""
			}
			return f.Names[i-idx].Name
		}
		idx += n
	}
	return ""
}

func stringLits(es []ast.Expr) []string {
	var out []string
	for _, e := range es {
		if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if s, err := strconv.Unquote(bl.Value); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// The note itself: a key the action does not take is named, with the action
// that does take it; a declared, accepted or framework key is not; and it
// reaches the model through the tool's AgentToolDef, after the result.
func TestUnusedParamsAreNamedToTheModel(t *testing.T) {
	g := NewGroupedTool("workspace", "files")
	g.AddAction("grep", &GroupedToolAction{
		Description: "search a file",
		Params: map[string]ToolParam{
			"path":    {Type: "string", Description: "file"},
			"pattern": {Type: "string", Description: "regex"},
		},
		Accepts: []string{"legacy"},
		Handler: func(map[string]any, *ToolSession) (string, error) { return "3 matches", nil },
	})
	g.AddAction("read_lines", &GroupedToolAction{
		Description: "page a file",
		Params: map[string]ToolParam{
			"path":   {Type: "string", Description: "file"},
			"offset": {Type: "integer", Description: "first line"},
		},
		Handler: func(map[string]any, *ToolSession) (string, error) { return "lines", nil },
	})

	note := g.unusedArgsNote(map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200, "Lines": 100})
	for _, want := range []string{`action="grep" does not take "Lines", "offset"`, `"offset", which belongs to action="read_lines"`, "Its params: path, pattern."} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	for _, args := range []map[string]any{
		{"action": "grep", "path": "a", "pattern": "x"},
		{"action": "grep", "PATH": "a", "pattern": "x", "legacy": true, "_carried": 1},
		{"path": "a", "offset": 1}, // inferred: every key is the inferred action's
		{"action": "nope", "x": 1}, // refused before it runs
		{"action": "help", "x": 1}, // help has its own banner
	} {
		if n := g.unusedArgsNote(args); n != "" {
			t.Errorf("%v: got a note for keys that were used or never ran:\n%s", args, n)
		}
	}

	def := ChatToolToAgentToolDefWithSession(g, nil)
	out, err := def.Handler(context.Background(), map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "3 matches") || !strings.Contains(out, "[PARAMS NOT USED]") {
		t.Errorf("the model's result should be the tool's, then the note:\n%s", out)
	}
	if direct, _ := g.RunWithSession(map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200}, nil); direct != "3 matches" {
		t.Errorf("a direct caller parses what the tool returns and must get it bare, got %q", direct)
	}
}
