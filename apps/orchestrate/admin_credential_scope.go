// Credential scoping ENFORCEMENT — the per-agent DENY-LIST that removes a
// credential (and every tool that dispatches through it) from an agent's kit.
// A credential is GLOBAL (every agent can dispatch through it) by default; the
// deny-list has two tiers: TIER 1 (on the credential) a GLOBAL credential with a
// non-empty AllowedUsers is denied to users not listed; TIER 2 (per agent) the
// agent's own DisabledCredentials opt-outs (enforced in setupCustomTools).
//
// The EDITING surfaces live elsewhere now: tier-1 "which users" on the admin
// credential page (Access button → AllowedUsers); tier-2 per-agent scope on the
// agent editor ("Credentials this agent may use", handleAgentCredentials). This
// file is enforcement only — it no longer registers a scope-pill provider.
package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

// credentialDenySet builds the set of credentials the agent — running in a
// session for user `user` — may NOT dispatch through. Two tiers:
//   - TIER 1 (on the credential): a GLOBAL credential with a non-empty
//     AllowedUsers is denied to users not listed. (User-OWNED credentials need no
//     entry here: they live in the owner's namespace and Secure().Resolve simply
//     won't find them for anyone else — namespace isolation IS the enforcement.)
//   - TIER 2 (per agent, on the record): the agent's own DisabledCredentials
//     opt-outs.
//
// `user` is the SESSION user, not agent.Owner — a system-owned seed agent runs on
// behalf of whoever is in the session. Populated onto every ToolSession that runs
// this agent's loop so the fetch_url auto-route enforces the same scope
// everywhere the agent can fetch. See docs/tool-credential-namespacing.md.
func credentialDenySet(a AgentRecord, user string) map[string]bool {
	var deny map[string]bool
	add := func(name string) {
		if deny == nil {
			deny = map[string]bool{}
		}
		deny[name] = true
	}
	// Tier 1: GLOBAL credentials whose AllowedUsers grant excludes this user.
	// (Secure().List() is the global namespace — user-owned creds aren't in it.)
	if user != "" {
		for _, c := range Secure().List() {
			if len(c.AllowedUsers) > 0 && !containsString(c.AllowedUsers, user) {
				add(c.Name)
			}
		}
	}
	// Tier 2: this agent's per-agent opt-outs.
	for _, c := range a.DisabledCredentials {
		add(c)
	}
	return deny
}
