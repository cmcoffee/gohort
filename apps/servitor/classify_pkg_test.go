package servitor

// Installing software was invisible to the classifier: "apt-get install -y
// anything" scored RiskNone, and cmd_allowed returns true for RiskNone
// unconditionally, so the single most powerful command on a box ran unprompted
// while rm and systemctl stop were gated.
//
// The split that has to hold in BOTH directions: an install is gated, and
// reading the package list is not — most of an investigation is reading, and a
// survey that stops for permission on "dpkg -l" teaches nobody anything.

import (
	"strings"
	"testing"
)

func TestInstallsAreGated(t *testing.T) {
	for _, cmd := range []string{
		"apt-get install -y nginx",
		"apt install nginx",
		"sudo apt-get upgrade",
		"yum install httpd",
		"dnf remove httpd",
		"apk add curl",
		"zypper install vim",
		"snap install docker",
		"pip install requests",
		"pip3 uninstall requests",
		"npm install -g typescript",
		"yarn add lodash",
		"gem install rails",
		"cargo install ripgrep",
		"go install example.com/x@latest",
		"dpkg -i package.deb",
		"rpm -i package.rpm",
		"nix-env -i hello",
	} {
		cat, reason := classify_command(cmd)
		if cat != RiskPkgInstall {
			t.Errorf("%q classified %q (%s), want pkg_install", cmd, cat, reason)
		}
	}
}

func TestReadingPackagesStaysUngated(t *testing.T) {
	for _, cmd := range []string{
		"dpkg -l",
		"apt list --installed",
		"apt-cache search nginx",
		"yum list installed",
		"rpm -qa",
		"pip list",
		"pip freeze",
		"npm ls",
		"npm --version",
		"apt show nginx",
		"snap list",
		"brew list",
	} {
		if cat, reason := classify_command(cmd); cat != RiskNone {
			t.Errorf("%q classified %q (%s) — reading the package list must not prompt", cmd, cat, reason)
		}
	}
}

// The prefix trap: "--installed" starts with "install". An earlier version of
// this table would have gated every "apt list --installed".
func TestInstalledFlagIsNotAnInstall(t *testing.T) {
	if cat, _ := classify_command("apt list --installed"); cat != RiskNone {
		t.Errorf("--installed was read as install (cat=%q)", cat)
	}
	if cat, _ := classify_command("apt install nginx"); cat != RiskPkgInstall {
		t.Error("a real install stopped matching")
	}
}

func TestPkgInstallIsGrantableLikeAnyCategory(t *testing.T) {
	var found bool
	for _, c := range AllRiskCategories {
		if c == RiskPkgInstall {
			found = true
		}
	}
	if !found {
		t.Fatal("pkg_install is not in AllRiskCategories, so it cannot be granted, shown, or passed to --allow")
	}
	// And it must survive a grant round trip like the others.
	if got := cleanCategories([]string{string(RiskPkgInstall)}); len(got) != 1 || got[0] != string(RiskPkgInstall) {
		t.Errorf("cleanCategories dropped pkg_install: %v", got)
	}
}

// A category nobody has ticked yet must default to prompting, so adding one
// cannot silently widen an existing deployment.
func TestNewCategoryDefaultsToPrompt(t *testing.T) {
	udb := grantStore(t)
	saveAllowedCategories(udb, map[string]bool{string(RiskSysControl): true})
	set := loadAllowedCategories(udb)
	if set[RiskPkgInstall] {
		t.Error("pkg_install auto-runs on a record written before it existed")
	}
	if !set[RiskSysControl] {
		t.Error("the pre-existing choice was lost")
	}
	if reason := strings.TrimSpace(string(RiskPkgInstall)); reason == "" {
		t.Error("category name is empty")
	}
}
