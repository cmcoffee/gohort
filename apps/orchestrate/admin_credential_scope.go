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

// openCredNames returns the names of every OPEN (non-secured) registered
// credential — the set the allow-list gates. Secured creds are excluded: their
// access follows the tool-binding model, not per-agent scope.
func openCredNames() []string {
	var out []string
	for _, c := range Secure().List() {
		if !c.Secured {
			out = append(out, c.Name)
		}
	}
	return out
}

// credentialDenySet builds the lookup set of credentials this agent may NOT
// dispatch through. Dual-path during the deny→allow migration:
//   - EnabledCredentials present (allow-list): deny = every open cred NOT in it,
//     so a newly-registered cred is denied by default (least privilege).
//   - EnabledCredentials nil (legacy): deny = DisabledCredentials, i.e. exactly
//     the old behavior, so an un-migrated agent is unchanged.
//
// Populated onto every ToolSession that runs this agent's loop so the fetch_url
// auto-route enforces the same scope everywhere the agent can fetch.
func credentialDenySet(a AgentRecord) map[string]bool {
	if a.CredAllowlist {
		allow := make(map[string]bool, len(a.EnabledCredentials))
		for _, c := range a.EnabledCredentials {
			allow[c] = true
		}
		var deny map[string]bool
		for _, name := range openCredNames() {
			if !allow[name] {
				if deny == nil {
					deny = map[string]bool{}
				}
				deny[name] = true
			}
		}
		return deny
	}
	if len(a.DisabledCredentials) == 0 {
		return nil
	}
	deny := make(map[string]bool, len(a.DisabledCredentials))
	for _, c := range a.DisabledCredentials {
		deny[c] = true
	}
	return deny
}

// migrateCredScope converts a legacy (nil EnabledCredentials) agent to the
// allow-list model, baking in its CURRENT effective access (every open cred it
// isn't denying) so it loses nothing, then clearing the deny-list. Idempotent —
// an already-migrated agent is returned unchanged. Called on any write to an
// agent's credential scope. A no-op if the credential store isn't ready yet, so
// enforcement never snapshots an empty (deny-all) list at startup.
func migrateCredScope(a AgentRecord) AgentRecord {
	if a.CredAllowlist {
		return a
	}
	open := openCredNames()
	if len(open) == 0 {
		// No open creds registered (or the store isn't ready) — snapshotting now
		// would bake a deny-all list. Leave legacy; enforcement uses the deny-list.
		return a
	}
	enabled := []string{}
	for _, name := range open {
		if !containsString(a.DisabledCredentials, name) {
			enabled = append(enabled, name)
		}
	}
	a.CredAllowlist = true
	a.EnabledCredentials = enabled
	a.DisabledCredentials = nil
	return a
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
		// Migrated (allow-list) agent → on = it's in the allow-list; legacy agent
		// → on = not in the deny-list (unchanged reading).
		on := true
		if a.CredAllowlist {
			on = containsString(a.EnabledCredentials, name)
		} else {
			on = !containsString(a.DisabledCredentials, name)
		}
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

	setOne := func(a AgentRecord) error {
		// Touching an agent's credential scope migrates it to the allow-list model
		// (baking in its current access), then toggles membership.
		a = migrateCredScope(a)
		if !a.CredAllowlist {
			// Store not ready to snapshot — fall back to the legacy deny-list toggle.
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
		if on {
			if containsString(a.EnabledCredentials, name) {
				return nil
			}
			a.EnabledCredentials = append(a.EnabledCredentials, name)
		} else {
			if !containsString(a.EnabledCredentials, name) {
				return nil
			}
			a.EnabledCredentials = removeString(a.EnabledCredentials, name)
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
