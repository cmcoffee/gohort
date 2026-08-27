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
	"strings"

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
	// Tier 2: this agent's per-agent opt-outs — minus SECURED credentials. The
	// editor's picker (userScopableCredentials) filters secured creds out and says
	// so on the page: their access follows their TOOL BINDINGS, not per-agent
	// scope. An opt-out recorded before a credential was secured therefore
	// survives on the record — invisible in the picker, unclearable from the UI —
	// while this set went on enforcing it, silently dropping every tool bound to
	// that cred from the kit and blocking its fetch_url auto-route. Honor the
	// contract the UI states rather than a leftover the owner can no longer see.
	for _, c := range a.DisabledCredentials {
		if credentialSecured(c, user) {
			continue
		}
		add(c)
	}
	return deny
}

// credentialSecured reports whether `name` resolves to a SECURED credential for
// session user `user` — the same user-shadows-global lookup dispatch itself uses,
// so a user-owned secured cred shadowing a global open one reads as secured here
// too. A name that resolves to nothing is not secured, which keeps a stale
// opt-out for a deleted credential denying (it costs nothing and stays
// conservative).
func credentialSecured(name, user string) bool {
	c, ok := Secure().Resolve(strings.TrimSpace(name), user)
	return ok && c.Secured
}
