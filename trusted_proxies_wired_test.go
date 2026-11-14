package main

import (
	"os"
	"regexp"
	"testing"
)

// The trusted-proxy list existed in netgate for months and was set only by
// tests, so a proxy on another host was never believed. Pinned where it is
// wired: the server start in gohort.go, from the ini's trusted_proxies.
func TestTrustedProxiesAreWiredFromTheIni(t *testing.T) {
	b, err := os.ReadFile("gohort.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`netgate\.TrustedProxiesFunc = func\(\) string \{\s*return global\.cfg\.Get\(cfgWebSection, "trusted_proxies"\)`).Match(b) {
		t.Error("netgate.TrustedProxiesFunc is not set from trusted_proxies at server start")
	}
	c, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`saveWebString\("trusted_proxies"`).Match(c) || !regexp.MustCompile(`(?m)^trusted_proxies =$`).Match(c) {
		t.Error("trusted_proxies is not in the setup menu's save or the ini template")
	}
}
