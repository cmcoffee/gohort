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
	"time"
)

const (
	authorizationsTable    = "delegation_authorizations" // <owner>:<id> -> Authorization
	delegationPreauthTable = "delegation_preauth"        // <owner>:<agent> -> bool
)

// Authorization is a pending Operator action awaiting the user's approval.
// Action selects what approval runs:
//   - "" / "delegate": delegate Brief to Agent (the default).
//   - "send_message":  text Handle the contents of Text via the phantom bridge.
type Authorization struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Action    string    `json:"action,omitempty"`
	Agent     string    `json:"agent"`            // delegate: target agent name/id
	Brief     string    `json:"brief"`            // delegate: what the delegate should do
	Handle    string    `json:"handle,omitempty"` // send_message: recipient handle
	Text      string    `json:"text,omitempty"`   // send_message: message body
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

// RunDelegation executes a delegation synchronously via the registered standing
// runner (the agent-execution closure) and records it to the run-ledger. It
// reuses the deny-by-default confirm posture — a delegated run never auto-
// approves high-consequence tools.
func RunDelegation(ctx context.Context, db Database, owner, agent, brief string) RunRecord {
	return executeStandingRun(ctx, db, StandingAgent{
		Owner: owner, Name: agent, AgentID: agent, Mission: brief,
	}, "delegation")
}
