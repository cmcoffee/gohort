// Credential scoping ENFORCEMENT — the per-agent DENY-LIST that removes a
// credential (and every tool that dispatches through it) from an agent's kit.
// Tier 3, on a user-owned credential: OnlyAgents, the owner's "Only these
// agents" from the API's Security panel.
// A credential is GLOBAL (every agent can dispatch through it) by default; the
// deny-list has two tiers: TIER 1 (on the credential) a GLOBAL credential with a
// non-empty AllowedUsers is denied to users not listed; TIER 2 (per agent) the
// agent's own DisabledCredentials opt-outs (enforced in setupCustomTools).
//
// The EDITING surfaces live elsewhere now: tier-1 "which users" on the admin
// credential page (Access button → AllowedUsers); tier-2 per-agent scope on the
// agent editor ("External credentials", handleAgentCredentials). This
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
	// A sub-agent's parents, nearest first (agent_family.go): it shares
	// their "Only these agents" grants, since it is listed in no picker a
	// grant is made in, and their opt-outs, since it may be narrower than
	// they are but never wider.
	var parents []AgentRecord
	if strings.TrimSpace(a.OwnedBy) != "" {
		db := agentStore(chFirst(a.Owner, user))
		for _, id := range agentAncestors(db, a.ID) {
			if p, ok := loadAgent(db, id); ok {
				parents = append(parents, p)
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
	optOuts := append([]string(nil), a.DisabledCredentials...)
	for _, p := range parents {
		optOuts = append(optOuts, p.DisabledCredentials...)
	}
	for _, c := range optOuts {
		if credentialSecured(c, user) {
			continue
		}
		add(c)
	}
	// Tier 3 (on the credential, the owner's own): an API of theirs narrowed
	// to some of their agents ("Only these agents") is denied to every other
	// agent. Fails closed: an agent not on the list is denied, whatever else
	// would have allowed it. Only the session user's own credentials: a key
	// somebody lent them is governed by the lend, not by the lender's agents.
	// Not Builder: the owner driving it reaches every API of theirs, and a
	// run another agent sent is held to that agent's list instead
	// (builder_delegation.go). A sub-agent is on the list when any of its
	// parents is.
	if user != "" && !isBuilderAgent(a.ID) {
		for _, c := range Secure().ListUser(user) {
			if len(c.OnlyAgents) == 0 || containsString(c.OnlyAgents, a.ID) {
				continue
			}
			listed := false
			for _, p := range parents {
				if containsString(c.OnlyAgents, p.ID) {
					listed = true
					break
				}
			}
			if !listed {
				add(c.Name)
			}
		}
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
