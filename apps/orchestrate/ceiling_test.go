package orchestrate

// A ceiling on this package's size, as core has (core/ceiling_test.go).
//
// orchestrate is where agent work lands by default, and it has grown to
// several hundred files in one package. Splitting it is not free: almost every
// part hangs methods on OrchestrateApp or chatTurn, which cannot cross a
// package boundary (see `go run ./scripts/cutmap apps/orchestrate <prefix>`).
// What can be done cheaply is to make growth a decision rather than a default.
//
// WHEN THIS FAILS, in order of preference:
//
//  1. Put the code where it belongs outside this package: a core subpackage
//     for something generic, or a new package for a part that does not need
//     OrchestrateApp's methods.
//  2. Fold it into an existing file of the part it belongs to.
//  3. Raise the number below, in the same commit, and say in the message why
//     it belongs here. That is a fine answer; it is just not a silent one.
//
// And when files leave, lower the number, so it keeps measuring something.

import (
	"os"
	"strings"
	"testing"
)

// orchestrateFileCeiling is the number of non-test .go files in this package.
//
// 447 as of v0.9.677, when the ceiling was added (doc.go, the package map,
// is one of them).
const orchestrateFileCeiling = 448

func TestOrchestrateStaysUnderItsCeiling(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	files := 0
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files++
		}
	}
	switch {
	case files > orchestrateFileCeiling:
		t.Errorf("package orchestrate is %d files, ceiling %d.\n"+
			"Prefer a home outside this package, or fold the code into its part's existing file (see doc.go for the parts).\n"+
			"If it belongs here, raise orchestrateFileCeiling to %d in ceiling_test.go and say why in the commit.",
			files, orchestrateFileCeiling, files)
	case files < orchestrateFileCeiling:
		t.Errorf("package orchestrate is down to %d files, ceiling still %d: lower orchestrateFileCeiling to %d "+
			"so it keeps measuring something.", files, orchestrateFileCeiling, files)
	}
}
