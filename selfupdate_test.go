package main

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/selfupdate"
)

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

// A program started with no address relaunches where it is listening, so an
// update never moves it; one started with an address keeps its own command
// line.
func TestAnUpdateKeepsTheAddressTheProgramListensOn(t *testing.T) {
	u := &selfupdate.Updater{}
	keepAddrAcrossUpdates(u, "", "127.0.0.1:8181", []string{"gohort", "serve"})
	if got := strings.Join(u.RelaunchArgs, " "); got != "gohort serve 127.0.0.1:8181" {
		t.Errorf("a bare serve relaunches as %q", got)
	}
	given := &selfupdate.Updater{}
	keepAddrAcrossUpdates(given, ":8080", ":8080", []string{"gohort", "serve", ":8080"})
	if given.RelaunchArgs != nil {
		t.Errorf("a serve with an address relaunches as %q", given.RelaunchArgs)
	}
	keepAddrAcrossUpdates(nil, "", ":8080", nil)
}
