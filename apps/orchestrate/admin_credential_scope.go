// Credential scoping — per-agent revocation of SecureAPI credentials. Unlike
// tools and pipelines, a credential is not owned by an agent; it lives in the
// deployment/user credential store and is GLOBAL (every agent can dispatch
// through it) by default. Scoping is therefore a pure per-agent DENY-LIST:
// AgentRecord.DisabledCredentials. When an agent denies a credential, every
// tool whose .Credential matches is dropped from that agent's kit
// (enforced in setupCustomTools). The "credential" ScopeProvider registered
// here drives the same pill UI as tools and pipelines.
package orchestrate

import (
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterScopeProvider("credential", ScopeProvider{State: credentialScopeState, Set: setCredentialScope})
}

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

// credentialScopeState builds the per-agent picture for a credential. Global
// is true when NO agent denies it (all allowed); the credential UI renders
// per-agent pills only, but Global stays meaningful for a "revoke all"
// convenience toggle.
func credentialScopeState(db Database, owner, name string) (ToolScopeState, bool) {
	st := ToolScopeState{Name: name, Agents: []ToolScopeAgent{}}
	if exists, _, _ := Secure().CredentialStatus(name); !exists {
		return st, false
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return st, false
	}
	allAllowed := true
	for _, a := range listAgents(udb, owner) {
		// Only top-level, user-managed agents are access targets. App agents are
		// excluded by IDENTITY (isAppAgent) — the Hidden proxy leaks for a VISIBLE
		// app agent — and sub-agents (OwnedBy) are managed via their parent.
		if a.Hidden || a.OwnedBy != "" || isAppAgent(a.ID) {
			continue
		}
		// Tier-2 per-agent view: on = this agent hasn't opted out of the cred.
		on := !containsString(a.DisabledCredentials, name)
		if !on {
			allAllowed = false
		}
		st.Agents = append(st.Agents, ToolScopeAgent{ID: a.ID, Name: a.Name, On: on})
	}
	st.Global = allAllowed
	return st, true
}

// setCredentialScope applies one pill toggle. A credential has no
// promote/demote — only allow/deny per agent:
//
//	target=global, on=true  → allow on every agent (clear all deny entries)
//	target=global, on=false → deny on every agent
//	target=<agent>, on       → allow / deny for that agent
func setCredentialScope(db Database, owner, name, target string, on bool) error {
	if exists, _, _ := Secure().CredentialStatus(name); !exists {
		return fmt.Errorf("credential %q not found", name)
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return fmt.Errorf("no agent store for user %q", owner)
	}

	// Tier-2 per-agent opt-out toggle. on → allow (remove the opt-out); off → deny
	// (add it). Tier-1 "which users" is managed on the credential (AllowedUsers).
	setOne := func(a AgentRecord) error {
		if on {
			if !containsString(a.DisabledCredentials, name) {
				return nil
			}
			a.DisabledCredentials = removeString(a.DisabledCredentials, name)
		} else {
			if containsString(a.DisabledCredentials, name) {
				return nil
			}
			a.DisabledCredentials = append(a.DisabledCredentials, name)
		}
		_, err := saveAgent(udb, a)
		return err
	}

	if target == "global" {
		for _, a := range listAgents(udb, owner) {
			// Global toggle applies only to the user-managed agents the pill shows
			// (see credentialScopeState); skip app agents (by identity) + sub-agents.
			if a.Hidden || a.OwnedBy != "" || isAppAgent(a.ID) {
				continue
			}
			if err := setOne(a); err != nil {
				return err
			}
		}
		return nil
	}

	a, ok := loadAgent(udb, target)
	if !ok {
		return fmt.Errorf("agent %q not found", target)
	}
	// No Owner-field equality guard here: the agent is already scoped to the
	// resolved store (agentUserDB(db, owner) + loadAgent), and this path is
	// admin-driven — an admin scopes credentials for ANY agent, including
	// sub-agents whose .Owner is a parent/different value. The equality check
	// wrongly rejected exactly those with "not your agent".
	return setOne(a)
}
