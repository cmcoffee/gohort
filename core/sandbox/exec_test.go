package sandbox

import (
	"strings"
	"testing"
)

// A run with the proxy grant gets HTTP(S)_PROXY through its relay, the token
// as the password, loopback kept off it, and the relay started first; only on
// a backend whose relay is the run's own, and only with a relay to use.
func TestProxyEnvPointsHTTPClientsAtTheRelay(t *testing.T) {
	relay := func() map[string]string {
		return map[string]string{"GOHORT_HOOK_PATH": "/h.sock", RelayPort: "41234", "GOHORT_RELAY_TOKEN": "tok"}
	}
	for _, sb := range []sandboxBackend{bwrapSandbox{}, seatbeltSandbox{}} {
		env := relay()
		cmd := proxyEnv(sb, env, "pip install x")
		if env["HTTPS_PROXY"] != "http://gohort:tok@127.0.0.1:41234" || env["http_proxy"] != env["HTTPS_PROXY"] || !strings.Contains(env["NO_PROXY"], "127.0.0.1") {
			t.Errorf("%s: env %v", sb.name(), env)
		}
		if !strings.HasSuffix(cmd, "; pip install x") || !strings.Contains(cmd, "_gohort_relay") {
			t.Errorf("%s: the relay starts before the command: %q", sb.name(), cmd)
		}
	}
	env := relay()
	if cmd := proxyEnv(noSandbox{}, env, "pip install x"); cmd != "pip install x" || env["HTTPS_PROXY"] != "" {
		t.Errorf("an unconfined run has the host's network, and no relay of its own: %q %v", cmd, env)
	}
	env = relay()
	delete(env, "GOHORT_RELAY_TOKEN")
	if cmd := proxyEnv(bwrapSandbox{}, env, "x"); cmd != "x" || env["HTTPS_PROXY"] != "" {
		t.Errorf("no token, no proxy: %q %v", cmd, env)
	}
}
