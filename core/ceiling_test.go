package core

// A ceiling on this package, because nothing else says no.
//
// core is where AppCore and the shared vocabulary live, so it is where every new
// subsystem lands by default: a peer transport, a face detector, a model
// provider. Between 2026-08-06 and 2026-08-22 it took on 58 files and 25,600
// lines with nothing removed, undoing in a week an extraction project that had
// spent weeks cutting it down. Every one of those additions was a reasonable
// local decision, and that is the problem — nothing made any of them a decision
// at all.
//
// This test is the prompt. It does not judge whether a file belongs here; it
// makes putting one here something a person has to type a number for.
//
// WHEN THIS FAILS, in order of preference:
//
//  1. Put the code in a subpackage. `go run ./scripts/cutmap core <prefix>`
//     says whether a cluster can leave and what it would cost; docs/core-cuts.md
//     ranks the candidates and records the two traps (methods pinned to a type
//     in another file, and outbound TYPE edges, which cannot become hooks).
//  2. Unexport what does not need to be exported. Every exported name here lands
//     in the namespace of 548 dot-importing files — that is how core.PlanStep
//     collided with orchestrate's own and forced a rename in v0.6.335.
//  3. Raise the number below, in the same commit, and say in the message why
//     this subsystem belongs in the hub. That is a legitimate answer. It is
//     just not a silent one.
//
// And when you cut something OUT, lower the number. A ceiling that stays above
// the real figure stops measuring anything, so this fails in that direction too.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

const (
	// coreFileCeiling is the number of non-test .go files in package core.
	// Hard: one new file is exactly the decision this exists to catch.
	coreFileCeiling = 166

	// coreExportCeiling is the number of exported top-level symbols — funcs,
	// types, vars, consts. Methods are excluded because they are not what
	// collides: a dot-import puts these names, and only these, into another
	// package's namespace.
	coreExportCeiling = 2109

	// coreExportSlack is a small band on the export count only. A file here
	// legitimately grows an exported helper or two during ordinary work, and a
	// test that fails on that gets raised reflexively until it means nothing.
	// A new subsystem's worth of API still trips it.
	coreExportSlack = 25
)

func TestCoreStaysUnderItsCeiling(t *testing.T) {
	files, exports := measureCorePackage(t)

	switch {
	case files > coreFileCeiling:
		t.Errorf("package core is %d files, ceiling %d.\n"+
			"Prefer a subpackage (see docs/core-cuts.md and `go run ./scripts/cutmap core <prefix>`).\n"+
			"If the hub is genuinely the right home, raise coreFileCeiling to %d in this file and say why in the commit.",
			files, coreFileCeiling, files)
	case files < coreFileCeiling:
		t.Errorf("package core is down to %d files, ceiling still %d — lower coreFileCeiling to %d "+
			"so it keeps measuring something.", files, coreFileCeiling, files)
	}

	switch {
	case exports > coreExportCeiling+coreExportSlack:
		t.Errorf("package core exports %d top-level symbols, ceiling %d (+%d slack).\n"+
			"Every one of these lands in the namespace of every file that dot-imports core.\n"+
			"Unexport what callers outside this package do not need, move the subsystem to a "+
			"subpackage, or raise coreExportCeiling to %d and say why.",
			exports, coreExportCeiling, coreExportSlack, exports)
	case exports < coreExportCeiling-coreExportSlack:
		t.Errorf("package core is down to %d exported symbols, ceiling still %d — lower "+
			"coreExportCeiling to %d.", exports, coreExportCeiling, exports)
	}
}

// measureCorePackage counts the non-test source files in this directory and the
// exported top-level symbols they declare.
//
// Parsed rather than grepped: a comment mentioning `func Foo` and a string
// containing "type Bar" both fool a regex, and a ceiling that miscounts is worse
// than none — it fails on work that did not change anything.
func measureCorePackage(t *testing.T) (files, exports int) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package core: %v", err)
	}
	pkg, ok := pkgs["core"]
	if !ok {
		t.Fatal("no package core in this directory — did the test move?")
	}
	for _, f := range pkg.Files {
		files++
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				// Methods are excluded: they hang off a type, not the package.
				if d.Recv == nil && d.Name.IsExported() {
					exports++
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							exports++
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								exports++
							}
						}
					}
				}
			}
		}
	}
	return files, exports
}
