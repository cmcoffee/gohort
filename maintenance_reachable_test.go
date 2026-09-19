package main

// Every maintenance group an app registers into has to reach a screen.
//
// A maintenance function declares a GROUP and nothing more. That group becomes
// visible only if some section in apps/admin calls maintenanceList for that
// exact string — two halves, written in different packages, joined by a literal
// nobody checks. When they disagree the function is registered, callable by its
// endpoint, and has no button: it appears nowhere and nothing warns.
//
// There IS a safety net: the list endpoint folds an unknown group into
// Housekeeping so no button can vanish. It is the right net and a very quiet
// one — the function appears, just not where its author said to look. The
// guardrail sweep registered under "Migrations", which the page did not lay
// out, so for a fortnight it sat in Housekeeping while a note sent operators to
// Migrations to find it.
//
// So reachable is not the only thing worth asserting. A group the page lays out
// must ALSO be known to the endpoint, or the list asks for a name the endpoint
// has already rewritten and renders empty — which is how adding the section
// without adding the name would have left the sweep exactly where it was while
// looking like a fix.
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

// Every registered function reaches SOME button: its own group's list, or the
// Housekeeping net.
func TestEveryMaintenanceFunctionReachesAButton(t *testing.T) {
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
		if !rendered[f.Group] && !rendered[maintenanceFallbackGroup(t)] {
			t.Errorf("%q is unreachable: group %q is not laid out and the fallback group is not either", f.Label, f.Group)
		}
	}
}

// And it reaches the button its author NAMED, rather than the net. A function
// in the net is findable but not where anything says it is, and the note that
// says otherwise is the thing people act on.
func TestAFunctionAppearsUnderTheGroupItNamed(t *testing.T) {
	rendered := renderedMaintenanceGroups(t)
	known := knownEndpointGroups(t)
	for _, f := range core.ListMaintenanceFuncs() {
		if f.Group == "" {
			continue
		}
		switch {
		case !rendered[f.Group]:
			t.Errorf("%q names group %q, which the admin page does not lay out: it falls into %s",
				f.Label, f.Group, maintenanceFallbackGroup(t))
		case !known[f.Group]:
			// The half that looks like a fix and is not: the section exists,
			// the endpoint renames the group before comparing, the list is
			// empty and the function is still in the net.
			t.Errorf("group %q is laid out but not in maintenanceGroups, so the endpoint rewrites it and the section renders empty", f.Group)
		}
	}
}

// knownEndpointGroups reads the endpoint's own list of groups it will not
// rewrite.
func knownEndpointGroups(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("apps/admin/api_maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`maintenanceGroups = \[\]string\{([^}]*)\}`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("could not read maintenanceGroups")
	}
	out := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		out[q[1]] = true
	}
	return out
}

// maintenanceFallbackGroup reads where an unknown group is folded to.
func maintenanceFallbackGroup(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("apps/admin/api_maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`maintenanceGroupOther = "([^"]+)"`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("could not read maintenanceGroupOther")
	}
	return m[1]
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
