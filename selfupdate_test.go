package main

import "testing"

// A build is marked by what it registers: private.go is not in the public
// repository, so it cannot say so itself.
func TestABuildWithThePrivateAppsIsNotOfferedThePublicUpdate(t *testing.T) {
	if hasPrivateApps([]string{"github.com/cmcoffee/gohort/apps/admin", "github.com/cmcoffee/gohort/tools/browser"}) {
		t.Error("a public build reads as private")
	}
	if !hasPrivateApps([]string{"github.com/cmcoffee/gohort/apps/admin", "github.com/cmcoffee/gohort/private/blogger"}) {
		t.Error("a build with a private app reads as public")
	}
	pkgs := registeredPackages()
	if len(pkgs) == 0 {
		t.Fatal("no registered packages found: the check would read every build as public")
	}
	for _, p := range pkgs {
		if p == "" {
			t.Errorf("a registered app or tool with no package: %v", pkgs)
			break
		}
	}
}
