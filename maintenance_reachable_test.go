package main

// Every maintenance group an app registers into has to reach a screen.
//
// A maintenance function declares a GROUP and nothing more. That group becomes
// visible only if some section in apps/admin calls maintenanceList for that
// exact string — two halves, written in different packages, joined by a literal
// nobody checks. When they disagree the function is registered, callable by its
// endpoint, and has no button: it appears nowhere and nothing warns.
//
// Not hypothetical. The guardrail person-exception sweep registered under
// "Migrations"; the Migrations section rendered a table of PAST runs and no
// action list. The note telling an operator to go and click it described a path
// that did not exist, and it sat unrun while the exemptions it was meant to
// move stayed inert.
//
// THIS TEST LIVES IN THE ROOT PACKAGE ON PURPOSE. Written inside apps/admin it
// passes for the wrong reason: that binary does not import apps/orchestrate, so
// the sweep's init never runs and ListMaintenanceFuncs returns nothing to check
// the page against. Only here, where the loader has pulled in every app, do
// both halves exist at once.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
)

var maintenanceListRe = regexp.MustCompile(`maintenanceList\(\s*"([^"]+)"`)

func renderedMaintenanceGroups(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("apps/admin/page_maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range maintenanceListRe.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("no maintenance lists render at all; the admin page has been reshaped")
	}
	return out
}

func TestEveryMaintenanceGroupHasAButton(t *testing.T) {
	rendered := renderedMaintenanceGroups(t)
	funcs := core.ListMaintenanceFuncs()
	if len(funcs) == 0 {
		t.Fatal("no maintenance functions are registered; the loader is not pulling the apps in")
	}
	for _, f := range funcs {
		if f.Group == "" {
			t.Errorf("maintenance function %q declares no group", f.Label)
			continue
		}
		if !rendered[f.Group] {
			t.Errorf("group %q has no button anywhere: %q is registered and unreachable", f.Group, f.Label)
		}
	}
}

// Matched exactly, so a difference of case or spacing fails the same way and
// looks like nothing at all.
func TestMaintenanceGroupNamesAgreeExactly(t *testing.T) {
	rendered := renderedMaintenanceGroups(t)
	for _, f := range core.ListMaintenanceFuncs() {
		if f.Group == "" || rendered[f.Group] {
			continue
		}
		for r := range rendered {
			if strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(f.Group)) {
				t.Errorf("group %q is registered but the page renders %q: they differ only in case or spacing", f.Group, r)
			}
		}
	}
}
