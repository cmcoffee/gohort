package orchestrate

import "testing"

// The "*" sentinel means "every tool". Downstream readers spell that as
// an EMPTY allowlist, so "*" has to collapse to nil on every write path.
//
// Regression: update_agent stored a literal ["*"], which is a strict
// allowlist of length one naming a tool that cannot exist. The agent came
// up with ZERO tools — the exact inverse of the request — and
// unresolvedAllowedTools skips "*" when hunting for typos, so nothing
// warned. The call reported "AGENT_UPDATED ok".
func TestNormalizeAllowedToolsWildcard(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"bare wildcard", []string{"*"}, nil},
		{"padded wildcard", []string{" * "}, nil},
		{"wildcard among names", []string{"web_search", "*"}, nil},
		{"explicit list untouched", []string{"web_search", "fetch_url"}, []string{"web_search", "fetch_url"}},
		{"none sentinel untouched", []string{noToolsSentinel}, []string{noToolsSentinel}},
		{"empty stays empty", []string{}, []string{}},
		{"nil stays nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeAllowedTools(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("normalizeAllowedTools(%q) = %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("normalizeAllowedTools(%q) = %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}

// An "everything" surface must read as everything to the code that
// decides what an agent can reach. This pins the contract the wildcard
// collapse depends on: empty means the default pool, and a one-element
// allowlist does NOT.
func TestEverythingSurfaceIsEmptyAllowlist(t *testing.T) {
	rec := &AgentRecord{AllowedTools: normalizeAllowedTools([]string{"*"})}
	if len(rec.AllowedTools) != 0 {
		t.Fatalf("wildcard did not collapse: %q", rec.AllowedTools)
	}
	// A literal wildcard left in place would have been a strict allowlist
	// that resolves to nothing, and would not even be reported as a typo.
	if missing := unresolvedAllowedTools(nil, &AgentRecord{AllowedTools: []string{"*"}}); len(missing) != 0 {
		t.Fatalf("unresolvedAllowedTools flagged %q — it deliberately skips the sentinel, which is why an unnormalized wildcard failed silently", missing)
	}
}

// The transcript case: a standing agent was bound to an agent whose
// allowlist is the "none" sentinel, for a mission that needed to fetch
// and send. It scheduled fine and could never do the job.
func TestAgentHasNoTools(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"none sentinel", AgentRecord{AllowedTools: []string{noToolsSentinel}}, true},
		{"padded sentinel", AgentRecord{AllowedTools: []string{" " + noToolsSentinel + " "}}, true},
		// Empty means the DEFAULT POOL, the opposite of none. Confusing
		// these would warn on every fully-capable agent.
		{"empty is everything", AgentRecord{AllowedTools: nil}, false},
		{"explicit list", AgentRecord{AllowedTools: []string{"web_search"}}, false},
		{"sentinel among names", AgentRecord{AllowedTools: []string{noToolsSentinel, "web_search"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentHasNoTools(c.rec); got != c.want {
				t.Errorf("agentHasNoTools(%q) = %v, want %v", c.rec.AllowedTools, got, c.want)
			}
		})
	}
}
