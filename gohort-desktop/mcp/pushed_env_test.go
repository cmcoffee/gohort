package mcp

import (
	"strings"
	"testing"
)

// A server-pushed MCP install could set any environment on the process it
// spawned. The user consents to a command and arguments; LD_PRELOAD and its
// kind run other code inside that command. Refused before anything starts.
func TestPushedEnvCannotChangeHowTheProgramLoads(t *testing.T) {
	for _, k := range []string{"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONSTARTUP", "PATH", "BASH_ENV", "BAD NAME", "A=B"} {
		if err := CheckPushedEnv(map[string]string{k: "x"}); err == nil {
			t.Errorf("%q was accepted", k)
		}
	}
	if err := CheckPushedEnv(map[string]string{"API_TOKEN": "x", "REGION": "y"}); err != nil {
		t.Errorf("ordinary values were refused: %v", err)
	}
	// Install refuses on its own, before spawning anything.
	err := Install("pushed", "/nonexistent/should-not-run", nil, map[string]string{"LD_PRELOAD": "/tmp/x.so"})
	if err == nil || !strings.Contains(err.Error(), "LD_PRELOAD") {
		t.Fatalf("Install should refuse the loader variable itself, got %v", err)
	}
}
