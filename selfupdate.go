package main

// Software update from the admin page (docs/design/software-update.md): where
// releases come from, which keys they are checked with, whether this build is
// offered them at all, and how the program restarts onto a new one.

import (
	"reflect"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/selfupdate"
)

// defaultUpdateURL is the dist folder release builds update from, the same
// site kitebroker's updates come from. [update] url in gohort.ini replaces it.
const defaultUpdateURL = "https://dist.snuglab.com/gohort"

const cfgUpdateSection = "update"

// privateBuildReason is why a build with the private apps in it is never
// offered the public update: installing it would drop them.
const privateBuildReason = "this build has the private apps in it, and a release from the dist site is the public build: updating would drop them. Update it from source."

// wireSelfUpdate sets up the admin page's updater. A bad key setting leaves
// it with no keys, so it offers nothing rather than trusting the wrong ones.
func wireSelfUpdate() {
	src := selfupdate.Source{
		URL:       strings.TrimSpace(global.cfg.Get(cfgUpdateSection, "url")),
		Product:   APPNAME,
		UserAgent: APPNAME + "/" + VERSION,
		Keys:      selfupdate.BuiltinKeys(),
	}
	if src.URL == "" {
		src.URL = defaultUpdateURL
	}
	if k := strings.TrimSpace(global.cfg.Get(cfgUpdateSection, "key")); k != "" {
		keys, err := selfupdate.ParseKeys(k)
		if err != nil {
			Warn("[update] [update] key in gohort.ini: %v: no release will be offered", err)
		}
		src.Keys = keys
	}
	in, err := selfupdate.Here()
	if err != nil {
		Warn("[update] the running program's file is not known: %v", err)
	}
	u := &selfupdate.Updater{Product: APPNAME, Version: VERSION, Source: src, Install: in}
	if hasPrivateApps(registeredPackages()) {
		u.Blocked = privateBuildReason
	}
	// Shut down the way an interrupt does, so the databases close; the
	// relaunch is the last thing that shutdown runs (see main).
	u.Restart = func() {
		Log("[update] restarting onto the installed version")
		ShutdownApp()
		Exit(0)
	}
	selfupdate.Default = u
}

// relaunchIfUpdated runs at the very end of shutdown: when an update or a
// rollback was installed, it starts that binary in place of this process.
func relaunchIfUpdated() {
	if err := selfupdate.RelaunchIfPending(); err != nil {
		Warn("[update] could not start the installed version: %v. Start gohort again by hand.", err)
	}
}

// registeredPackages is the package of every registered app and chat tool.
func registeredPackages() []string {
	var out []string
	add := func(v any) {
		t := reflect.TypeOf(v)
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t != nil {
			out = append(out, t.PkgPath())
		}
	}
	for _, a := range RegisteredApps() {
		add(a)
	}
	for _, c := range RegisteredChatTools() {
		add(c)
	}
	return out
}

// hasPrivateApps reports whether any package is from the private tree.
// private.go is not in the public repository, so it cannot mark the build
// itself; what it imports registers apps, and their packages say where they
// are from.
func hasPrivateApps(pkgs []string) bool {
	for _, p := range pkgs {
		if strings.Contains(p, "/gohort/private/") {
			return true
		}
	}
	return false
}
