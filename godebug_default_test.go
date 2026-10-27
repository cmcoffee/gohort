package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// gohort.go pins the GODEBUG defaults so a GOPATH-mode build, which ignores
// go.mod, behaves as go.mod's go line says. A go line moved without the pin
// leaves that build on the old defaults again, so the two are kept equal.
func TestTheGODEBUGPinFollowsGoMod(t *testing.T) {
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("gohort.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("go.mod has no go line")
	}
	want := "//go:debug default=go" + string(m[1]) + "." + string(m[2])
	if !strings.Contains(string(src), want+"\n") {
		t.Errorf("gohort.go does not pin %q, the go line in go.mod", strings.TrimPrefix(want, "//go:debug "))
	}
}
