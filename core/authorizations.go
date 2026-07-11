// Delegation authorization (model A): the Operator is a controller — it acts
// by DELEGATING to agents, and delegation is gated. A delegation to a
// pre-authorized agent runs immediately; anything else lands in the
// authorizations queue for the user's approval.
//
// This is the data + execution spine (owner-scoped, reusing the run-ledger and
// the registered standing runner). The Operator's gated `delegate` tool and
// the Authorizations pane (orchestrate) sit on top.

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	authorizationsTable    = "delegation_authorizations" // <owner>:<id> -> Authorization
	delegationPreauthTable = "delegation_preauth"        // <owner>:<agent> -> bool (legacy "allow")
	contactPreauthTable    = "contact_preauth"           // <owner>:<handle> -> bool (legacy "allow")
	delegationPolicyTable  = "delegation_policy"         // <owner>:<agent> -> policy string
	contactPolicyTable     = "contact_policy"            // <owner>:<handle> -> policy string
)

// Permission policy states for delegations + contacts (the Permissions settings
// model). The legacy preauth bool maps to PolicyAllow; absence defaults to
// PolicyAsk; PolicyBlock auto-denies at the gate.
const (
	PolicyAllow = "allow" // act without asking — safe to run unattended (background)
	PolicyAsk   = "ask"   // queue / live-prompt; denied when nobody's present
	PolicyBlock = "block" // auto-deny, never run, never queue
)

// Authorization is a pending Operator action awaiting the user's approval.
// Action selects what approval runs:
//   - "" / "delegate":     delegate Brief to Agent (the default).
//   - "send_message":      text Handle the contents of Text via the phantom bridge.
//   - "converse_contact":  hand a GOAL (Brief) to phantom and let it run an
//     autonomous multi-turn conversation with Handle — one
//     upfront approval authorizes the whole exchange, not
//     each message. Reuses Handle (recipient) + Brief (goal).
type Authorization struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Action    string    `json:"action,omitempty"`
	Agent     string    `json:"agent"`             // delegate: target agent name/id
	Brief     string    `json:"brief"`             // delegate: what the delegate should do; converse_contact: the goal
	ChatID    string    `json:"chat_id,omitempty"` // send_message / converse_contact: target conversation (preferred; REQUIRED for groups)
	Handle    string    `json:"handle,omitempty"`  // send_message / converse_contact: recipient handle (new-individual fallback)
	Text      string    `json:"text,omitempty"`    // send_message: message body
	Images    []string  `json:"images,omitempty"`  // send_message: base64 attachments captured at queue time (survive until approval)
	Requested time.Time `json:"requested"`
}

func authKey(owner, id string) string { return owner + ":" + id }

func newAuthID(owner, agent string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", owner, agent, time.Now().UnixNano())))
	return hex.EncodeToString(h[:6])
}

// SaveAuthorization queues a pending delegation (fills ID/Requested when unset).
func SaveAuthorization(db Database, a Authorization) Authorization {
	if db == nil || a.Owner == "" {
		return a
	}
	if a.ID == "" {
		a.ID = newAuthID(a.Owner, a.Agent)
	}
	if a.Requested.IsZero() {
		a.Requested = time.Now()
	}
	db.Set(authorizationsTable, authKey(a.Owner, a.ID), a)
	return a
}

func GetAuthorization(db Database, owner, id string) (Authorization, bool) {
	if db == nil || owner == "" || id == "" {
		return Authorization{}, false
	}
	var a Authorization
	if !db.Get(authorizationsTable, authKey(owner, id), &a) {
		return Authorization{}, false
	}
	return a, true
}

// ListAuthorizations returns the owner's pending delegations, newest first.
func ListAuthorizations(db Database, owner string) []Authorization {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []Authorization
	for _, k := range db.Keys(authorizationsTable) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var a Authorization
		if db.Get(authorizationsTable, k, &a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requested.After(out[j].Requested) })
	return out
}

func DeleteAuthorization(db Database, owner, id string) {
	if db != nil {
		db.Unset(authorizationsTable, authKey(owner, id))
	}
}

func preauthKey(owner, agent string) string { return owner + ":" + agent }

// IsDelegationPreAuthorized reports whether the user has granted standing
// authorization for the Operator to delegate to this agent (model A: authorize
// the pattern). When true, a delegation runs immediately instead of queuing.
func IsDelegationPreAuthorized(db Database, owner, agent string) bool {
	if db == nil {
		return false
	}
	var on bool
	db.Get(delegationPreauthTable, preauthKey(owner, agent), &on)
	return on
}

func SetDelegationPreAuthorized(db Database, owner, agent string, on bool) {
	if db == nil {
		return
	}
	if on {
		db.Set(delegationPreauthTable, preauthKey(owner, agent), true)
	} else {
		db.Unset(delegationPreauthTable, preauthKey(owner, agent))
	}
}

// ListDelegationPreAuthorizations returns the agents the owner has granted
// standing delegation authorization to (the "Always allow" agents), sorted.
func ListDelegationPreAuthorizations(db Database, owner string) []string {
	return listPreauthTargets(db, delegationPreauthTable, owner)
}

// ListContactPreAuthorizations returns the contact handles the owner has
// granted standing messaging authorization to, sorted.
func ListContactPreAuthorizations(db Database, owner string) []string {
	return listPreauthTargets(db, contactPreauthTable, owner)
}

// listPreauthTargets scans a preauth table for the owner's granted (true)
// keys and returns the target portion (the part after "<owner>:").
func listPreauthTargets(db Database, table, owner string) []string {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []string
	for _, k := range db.Keys(table) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var on bool
		if db.Get(table, k, &on) && on {
			out = append(out, k[len(prefix):])
		}
	}
	sort.Strings(out)
	return out
}

func contactPreauthKey(owner, handle string) string { return owner + ":" + strings.TrimSpace(handle) }

// IsContactPreAuthorized reports whether the user has granted standing
// authorization for the Operator to message this contact handle via phantom
// (set by "Always allow" on a send_message / converse_contact approval). When
// true, both one-shot texts and autonomous conversations to this handle run
// immediately instead of queuing. Scope is PER CONTACT — a new handle still
// queues. (The grant is shared across the two messaging actions by design.)
func IsContactPreAuthorized(db Database, owner, handle string) bool {
	if db == nil || owner == "" || strings.TrimSpace(handle) == "" {
		return false
	}
	var on bool
	db.Get(contactPreauthTable, contactPreauthKey(owner, handle), &on)
	return on
}

func SetContactPreAuthorized(db Database, owner, handle string, on bool) {
	if db == nil || owner == "" || strings.TrimSpace(handle) == "" {
		return
	}
	if on {
		db.Set(contactPreauthTable, contactPreauthKey(owner, handle), true)
	} else {
		db.Unset(contactPreauthTable, contactPreauthKey(owner, handle))
	}
}

// PolicyEntry pairs a permission subject (agent id or contact handle) with its
// current policy, for the Permissions settings list.
type PolicyEntry struct {
	Target string
	Policy string
}

func normPolicy(p string) string {
	if p == PolicyAllow || p == PolicyAsk || p == PolicyBlock {
		return p
	}
	return PolicyAsk
}

// DelegationPolicy resolves the standing policy for delegating to this agent.
// An explicit policy record wins; otherwise a legacy "Always allow" grant reads
// as allow, and the default is ask.
func DelegationPolicy(db Database, owner, agent string) string {
	if db == nil {
		return PolicyAsk
	}
	var p string
	if db.Get(delegationPolicyTable, preauthKey(owner, agent), &p) && p != "" {
		return normPolicy(p)
	}
	var on bool
	if db.Get(delegationPreauthTable, preauthKey(owner, agent), &on) && on {
		return PolicyAllow
	}
	return PolicyAsk
}

// SetDelegationPolicy records the standing policy and keeps the legacy preauth
// flag in sync (so ListDelegationPreAuthorizations / IsDelegationPreAuthorized
// stay correct without every caller learning about policies).
func SetDelegationPolicy(db Database, owner, agent, policy string) {
	if db == nil || strings.TrimSpace(agent) == "" {
		return
	}
	policy = normPolicy(policy)
	db.Set(delegationPolicyTable, preauthKey(owner, agent), policy)
	if policy == PolicyAllow {
		db.Set(delegationPreauthTable, preauthKey(owner, agent), true)
	} else {
		db.Unset(delegationPreauthTable, preauthKey(owner, agent))
	}
}

// RemoveDelegationPolicy forgets this agent entirely — back to the ask default
// with no record, so it drops out of the Permissions list.
func RemoveDelegationPolicy(db Database, owner, agent string) {
	if db == nil {
		return
	}
	db.Unset(delegationPolicyTable, preauthKey(owner, agent))
	db.Unset(delegationPreauthTable, preauthKey(owner, agent))
}

// IsDelegationBlocked reports whether delegations to this agent are blocked
// (auto-deny at the gate).
func IsDelegationBlocked(db Database, owner, agent string) bool {
	return DelegationPolicy(db, owner, agent) == PolicyBlock
}

// ListDelegationPolicies returns every agent with an explicit policy record,
// unioned with legacy allow grants, sorted by target.
func ListDelegationPolicies(db Database, owner string) []PolicyEntry {
	return listPolicies(db, delegationPolicyTable, owner, ListDelegationPreAuthorizations(db, owner))
}

// ContactPolicy / SetContactPolicy / RemoveContactPolicy / IsContactBlocked /
// ListContactPolicies mirror the delegation versions for phantom messaging.
func ContactPolicy(db Database, owner, handle string) string {
	if db == nil || strings.TrimSpace(handle) == "" {
		return PolicyAsk
	}
	var p string
	if db.Get(contactPolicyTable, contactPreauthKey(owner, handle), &p) && p != "" {
		return normPolicy(p)
	}
	var on bool
	if db.Get(contactPreauthTable, contactPreauthKey(owner, handle), &on) && on {
		return PolicyAllow
	}
	return PolicyAsk
}

func SetContactPolicy(db Database, owner, handle, policy string) {
	if db == nil || strings.TrimSpace(handle) == "" {
		return
	}
	policy = normPolicy(policy)
	db.Set(contactPolicyTable, contactPreauthKey(owner, handle), policy)
	if policy == PolicyAllow {
		db.Set(contactPreauthTable, contactPreauthKey(owner, handle), true)
	} else {
		db.Unset(contactPreauthTable, contactPreauthKey(owner, handle))
	}
}

func RemoveContactPolicy(db Database, owner, handle string) {
	if db == nil {
		return
	}
	db.Unset(contactPolicyTable, contactPreauthKey(owner, handle))
	db.Unset(contactPreauthTable, contactPreauthKey(owner, handle))
}

func IsContactBlocked(db Database, owner, handle string) bool {
	return ContactPolicy(db, owner, handle) == PolicyBlock
}

func ListContactPolicies(db Database, owner string) []PolicyEntry {
	return listPolicies(db, contactPolicyTable, owner, ListContactPreAuthorizations(db, owner))
}

// listPolicies merges explicit policy records with legacy allow grants.
func listPolicies(db Database, table, owner string, legacyAllow []string) []PolicyEntry {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	seen := map[string]string{}
	for _, k := range db.Keys(table) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var p string
		if db.Get(table, k, &p) && p != "" {
			seen[k[len(prefix):]] = normPolicy(p)
		}
	}
	for _, t := range legacyAllow {
		if _, ok := seen[t]; !ok {
			seen[t] = PolicyAllow
		}
	}
	out := make([]PolicyEntry, 0, len(seen))
	for t, p := range seen {
		out = append(out, PolicyEntry{Target: t, Policy: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// RunDelegation executes a delegation synchronously via the registered standing
// runner (the agent-execution closure) and records it to the run-ledger. It
// reuses the deny-by-default confirm posture — a delegated run never auto-
// approves high-consequence tools.
func RunDelegation(ctx context.Context, db Database, owner, agent, brief string) RunRecord {
	return executeStandingRun(ctx, db, StandingAgent{
		Owner: owner, Name: agent, AgentID: agent, Mission: brief,
	}, "delegation")
}
