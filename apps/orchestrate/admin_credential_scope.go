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

// credentialDenySet builds the lookup set for an agent's denied credentials,
// or nil when it denies none. Populated onto every ToolSession that runs this
// agent's loop (the interactive turn AND sub-agent/dispatch sessions) so the
// fetch_url auto-route enforces the same scope everywhere the agent can fetch.
func credentialDenySet(a AgentRecord) map[string]bool {
	if len(a.DisabledCredentials) == 0 {
		return nil
	}
	deny := make(map[string]bool, len(a.DisabledCredentials))
	for _, c := range a.DisabledCredentials {
		deny[c] = true
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
