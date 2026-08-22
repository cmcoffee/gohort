// cutmap answers one question: could these files leave this package?
//
//	go run ./scripts/cutmap core peer_          # a prefix cluster
//	go run ./scripts/cutmap core plan machine   # several prefixes at once
//	SYMS=1 go run ./scripts/cutmap core peer_   # every outbound edge, named
//
// For each file in the cluster it prints OUT (the other files in the package it
// reaches into) and PINNED (methods it declares on a type that lives in another
// file — Go will not let those cross a package boundary, so a pinned file cannot
// move without its type). Then it prints IN: who outside the cluster uses it,
// and which symbols. A cluster with a small IN surface and a fixed OUT set is a
// package waiting to happen; a pinned one is a dependency-inversion project.
//
// It exists because this analysis has now been written from scratch twice, and
// both times it was wrong in the same two ways before being fixed:
//
//   - A bare ast.Inspect counts `x.Field` selectors, composite-literal keys and
//     parameter names as references to any top-level symbol sharing the name,
//     which invents edges that are not there.
//   - A top-level `var _ = …` puts "_" in the symbol table, so every blank
//     identifier in the package reads as an edge to that file.
//
// Both are handled below. Committing the tool is how the corrections stay
// bought.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: cutmap <package-dir> <file-prefix>...  (SYMS=1 to name every outbound edge)")
		os.Exit(2)
	}
	dir := os.Args[1]
	prefixes := os.Args[2:]
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		fmt.Println("parse:", err)
		os.Exit(1)
	}
	declFile := map[string]string{} // top-level symbol -> file
	declKind := map[string]string{} // top-level symbol -> func | type | value
	typeFile := map[string]string{} // type name -> file
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			base := filepath.Base(path)
			files[base] = f
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil {
						declFile[d.Name.Name] = base
						declKind[d.Name.Name] = "func"
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							declFile[s.Name.Name] = base
							declKind[s.Name.Name] = "type"
							typeFile[s.Name.Name] = base
						case *ast.ValueSpec:
							for _, n := range s.Names {
								declFile[n.Name] = base
								declKind[n.Name] = "value"
							}
						}
					}
				}
			}
		}
	}
	inCluster := func(base string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(base, p) {
				return true
			}
		}
		return false
	}
	recvType := func(fd *ast.FuncDecl) string {
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return ""
		}
		switch t := fd.Recv.List[0].Type.(type) {
		case *ast.Ident:
			return t.Name
		case *ast.StarExpr:
			if id, ok := t.X.(*ast.Ident); ok {
				return id.Name
			}
		}
		return ""
	}
	out := map[string]map[string]bool{} // clusterfile -> other files it references
	outSyms := map[string]map[string]bool{}
	// Outbound edges by KIND, which is what decides whether a cut is possible.
	// A func or a value can be inverted with a hook var the host assigns; a TYPE
	// cannot — the leaf may not import the package it is leaving, so a type it
	// speaks has to move with it or be restated as a local interface. Counting
	// edges without splitting these apart is how a cluster reads movable and
	// turns out to be shared vocabulary.
	outKind := map[string]map[string]bool{"type": {}, "func": {}, "value": {}}
	in := map[string]map[string]bool{} // outsider file -> cluster symbols it uses
	pinned := map[string][]string{}    // clusterfile -> methods on foreign types
	clusterSyms := map[string]bool{}
	for base := range files {
		if !inCluster(base) {
			continue
		}
		for sym, f := range declFile {
			if f == base {
				clusterSyms[sym] = true
			}
		}
	}
	for base, f := range files {
		cluster := inCluster(base)
		if cluster {
			out[base] = map[string]bool{}
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					if rt := recvType(fd); rt != "" {
						if home, known := typeFile[rt]; known && !inCluster(home) {
							pinned[base] = append(pinned[base], rt+"."+fd.Name.Name)
						}
					}
				}
			}
		}
		use := func(name string) {
			if name == "_" {
				return // a top-level `var _ = …` makes every blank a false edge
			}
			home, known := declFile[name]
			if !known || home == base {
				return
			}
			if cluster && !inCluster(home) {
				out[base][home] = true
				if outSyms[base] == nil {
					outSyms[base] = map[string]bool{}
				}
				outSyms[base][home+":"+name] = true
				if k := declKind[name]; k != "" {
					outKind[k][name] = true
				}
			}
			if !cluster && inCluster(home) {
				if in[base] == nil {
					in[base] = map[string]bool{}
				}
				in[base][name] = true
			}
		}
		// Walk EXPRESSION positions only. A bare ast.Inspect counts x.Field
		// selectors, composite-literal keys and parameter names as references
		// to any top-level symbol that happens to share the name, which
		// invents edges — the trap the earlier mapping pass recorded.
		var walk func(ast.Node)
		walkList := func(ns []ast.Expr) {
			for _, n := range ns {
				walk(n)
			}
		}
		walk = func(n ast.Node) {
			switch t := n.(type) {
			case nil:
				return
			case *ast.Ident:
				use(t.Name)
				return
			case *ast.SelectorExpr:
				walk(t.X) // the Sel is a field/method name, not a package symbol
				return
			case *ast.KeyValueExpr:
				walk(t.Value) // a composite-literal key is a field name
				return
			case *ast.FuncDecl:
				if t.Recv != nil {
					for _, fld := range t.Recv.List {
						walk(fld.Type)
					}
				}
				walk(t.Type)
				if t.Body != nil {
					walk(t.Body)
				}
				return
			case *ast.FuncType:
				for _, grp := range []*ast.FieldList{t.Params, t.Results} {
					if grp == nil {
						continue
					}
					for _, fld := range grp.List {
						walk(fld.Type) // names are locals
					}
				}
				return
			case *ast.StructType:
				if t.Fields != nil {
					for _, fld := range t.Fields.List {
						walk(fld.Type)
					}
				}
				return
			case *ast.InterfaceType:
				if t.Methods != nil {
					for _, fld := range t.Methods.List {
						walk(fld.Type)
					}
				}
				return
			case *ast.CallExpr:
				walk(t.Fun)
				walkList(t.Args)
				return
			}
			ast.Inspect(n, func(c ast.Node) bool {
				if c == nil || c == n {
					return true
				}
				switch c.(type) {
				case *ast.Ident, *ast.SelectorExpr, *ast.KeyValueExpr, *ast.FuncDecl,
					*ast.FuncType, *ast.StructType, *ast.InterfaceType, *ast.CallExpr:
					walk(c)
					return false
				}
				return true
			})
		}
		for _, d := range f.Decls {
			walk(d)
		}
	}
	var names []string
	for b := range out {
		names = append(names, b)
	}
	sort.Strings(names)
	fmt.Printf("== cluster %v: %d files ==\n", prefixes, len(names))
	for _, b := range names {
		var refs []string
		for f := range out[b] {
			refs = append(refs, f)
		}
		sort.Strings(refs)
		show := refs
		if len(show) > 6 {
			show = append(append([]string{}, refs[:6]...), "…")
		}
		fmt.Printf("%-34s OUT=%-3d %v", b, len(refs), show)
		if p := pinned[b]; len(p) > 0 {
			fmt.Printf("  PINNED=%v", p)
		}
		fmt.Println()
	}
	if os.Getenv("SYMS") != "" {
		for _, b := range names {
			var s []string
			for k := range outSyms[b] {
				s = append(s, k)
			}
			sort.Strings(s)
			fmt.Printf("  %s -> %v\n", b, s)
		}
	}
	fmt.Printf("\nOUT edges by kind: %d type(s) (each must MOVE or be restated — cannot be a hook), %d func(s), %d value(s)\n",
		len(outKind["type"]), len(outKind["func"]), len(outKind["value"]))
	if len(outKind["type"]) > 0 {
		var ts []string
		for t := range outKind["type"] {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		fmt.Printf("  types: %v\n", ts)
	}
	fmt.Printf("\n-- who in core uses this cluster (IN) --\n")
	type row struct {
		file string
		syms []string
	}
	var rows []row
	for f, syms := range in {
		var s []string
		for x := range syms {
			s = append(s, x)
		}
		sort.Strings(s)
		rows = append(rows, row{f, s})
	}
	sort.Slice(rows, func(i, j int) bool { return len(rows[i].syms) > len(rows[j].syms) })
	for i, r := range rows {
		if i >= 12 {
			fmt.Printf("… and %d more files\n", len(rows)-12)
			break
		}
		show := r.syms
		if len(show) > 8 {
			show = append(append([]string{}, r.syms[:8]...), "…")
		}
		fmt.Printf("%-34s %d: %v\n", r.file, len(r.syms), show)
	}
}
