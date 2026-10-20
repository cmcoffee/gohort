package sandbox

// A caller's environment variables never reach a launcher that runs on the
// host, and the names that change which code loads reach nothing at all.
//
// The launcher is the hazard: bwrap and the podman client start on the host
// before any namespace exists, and the dynamic loader reads LD_PRELOAD from
// their environment. These tests pin both halves of env_guard.go, each of which
// closes that alone.

import (
	"context"
	"strings"
	"testing"
)

func hostileEnv() map[string]string {
	return map[string]string{
		"LD_PRELOAD":            "/ws/evil.so",
		"LD_AUDIT":              "/ws/evil.so",
		"GLIBC_TUNABLES":        "glibc.malloc.check=3",
		"DYLD_INSERT_LIBRARIES": "/ws/evil.dylib",
		"greeting":              "hi",
	}
}

func envHas(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

func TestABwrapLauncherGetsNoCallerVariables(t *testing.T) {
	withBackend(t, bwrapSandbox{path: "/usr/bin/bwrap"})
	built, err := NewSandboxedShellCmd(context.Background(), "true", t.TempDir(), hostileEnv())
	if err != nil {
		t.Fatal(err)
	}
	c := built.Cmd
	for _, k := range []string{"greeting", "LD_PRELOAD", "LD_AUDIT", "GLIBC_TUNABLES", "DYLD_INSERT_LIBRARIES"} {
		if envHas(c.Env, k) {
			t.Errorf("bwrap's own environment carries %s; it runs on the host, unconfined", k)
		}
	}
	argv := strings.Join(c.Args, " ")
	if !strings.Contains(argv, "--setenv greeting hi") {
		t.Errorf("an ordinary variable should still reach the command through --setenv:\n%s", argv)
	}
	for _, k := range []string{"LD_PRELOAD", "LD_AUDIT", "GLIBC_TUNABLES", "DYLD_INSERT_LIBRARIES"} {
		if strings.Contains(argv, k) {
			t.Errorf("%s should be dropped, not passed inside:\n%s", k, argv)
		}
	}
}

func TestAnInheritingBackendGetsOnlySafeVariables(t *testing.T) {
	withBackend(t, noSandbox{})
	t.Setenv("GOHORT_ALLOW_UNSANDBOXED", "on")
	built, err := NewSandboxedShellCmd(context.Background(), "true", t.TempDir(), hostileEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(built.Cmd.Env, "greeting") {
		t.Error("with no argv to carry it, an ordinary variable has to be on the process environment")
	}
	for _, k := range []string{"LD_PRELOAD", "LD_AUDIT", "GLIBC_TUNABLES", "DYLD_INSERT_LIBRARIES"} {
		if envHas(built.Cmd.Env, k) {
			t.Errorf("%s reached the process environment", k)
		}
	}
}

func TestABwrapPipeCarriesItsVariablesInTheArgv(t *testing.T) {
	c := bwrapSandbox{path: "/usr/bin/bwrap"}.build(context.Background(), sandboxRun{
		Kind: sandboxPipeRun, Command: "cat", Env: map[string]string{"start_line": "3"},
	})
	argv := strings.Join(c.Args, " ")
	set := strings.Index(argv, "--setenv start_line 3")
	sep := strings.Index(argv, " -- ")
	if set < 0 || sep < 0 || set > sep {
		t.Errorf("a pipe's variables belong before the separator as --setenv:\n%s", argv)
	}
}

// Each backend states whether its command inherits the launcher's environment.
// Pinned here so a new backend, or a change to one, is a decision about the
// launcher and not a default.
func TestEveryBackendSaysHowItsEnvironmentTravels(t *testing.T) {
	for _, tc := range []struct {
		sb   sandboxBackend
		want bool
	}{
		{bwrapSandbox{}, false},
		{containerSandbox{}, false},
		{noSandbox{}, true},
		{seatbeltSandbox{}, true},
	} {
		if got := tc.sb.inheritsEnv(); got != tc.want {
			t.Errorf("%s: inheritsEnv = %v, want %v", tc.sb.name(), got, tc.want)
		}
	}
}

func TestSafeExtraEnvLeavesTheCallersMapAlone(t *testing.T) {
	in := hostileEnv()
	out := safeExtraEnv(in)
	if _, ok := in["LD_PRELOAD"]; !ok {
		t.Error("safeExtraEnv modified the caller's map")
	}
	if len(out) != 1 || out["greeting"] != "hi" {
		t.Errorf("want only greeting kept, got %v", out)
	}
}
