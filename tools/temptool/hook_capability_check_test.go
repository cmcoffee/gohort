package temptool

// The undeclared-hook check. A repairing agent swapped fetch_url for
// browse_page in a script and left hook_capabilities alone; the call was
// refused at dispatch, the script died on what it did with the result, and the
// agent concluded the remote SITE was blocking it. Static and deterministic —
// the same class of check as the syntax pass.

import "testing"

func TestScriptCallsHookSpotsBothImportAndCall(t *testing.T) {
	cases := []struct {
		body, name string
		want       bool
	}{
		{"from gohort import browse_page\nx = browse_page(u)", "browse_page", true},
		{"from gohort import fetch_url, log", "log", true},
		{"resp = gohort.fetch_via(\"cred\", url)", "fetch_via", true},
		{"from gohort import fetch_url\nx = fetch_url(u)", "browse_page", false},
		{"", "fetch_url", false},
	}
	for _, c := range cases {
		if got := scriptCallsHook(c.body, c.name); got != c.want {
			t.Errorf("scriptCallsHook(%q, %q) = %v, want %v", c.body, c.name, got, c.want)
		}
	}
}

func TestHookCapabilityDeclaredMatchesTheServerGate(t *testing.T) {
	// Must mirror SandboxHook.granted: bare name, or any qualified form.
	caps := []string{"fetch", "fetch_via:openweather", " secret:apikey "}
	for _, want := range []string{"fetch", "fetch_via", "secret"} {
		if !hookCapabilityDeclared(caps, want) {
			t.Errorf("%q should count as declared by %v", want, caps)
		}
	}
	if hookCapabilityDeclared(caps, "browse_page") {
		t.Error("browse_page is not in that list — the check must not pass it")
	}
	// "fetch" must not satisfy "fetch_via": prefix matching runs the other way
	// (capability may be qualified, the wanted method never is).
	if hookCapabilityDeclared([]string{"fetch"}, "fetch_via") {
		t.Error("a bare fetch grant must not satisfy fetch_via")
	}
}
