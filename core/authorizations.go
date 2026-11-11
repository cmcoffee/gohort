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
	"net"
	neturl "net/url"
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
	Text      string    `json:"text,omitempty"`    // send_message: message body; approve_change: the work the change was part of
	Images    []string  `json:"images,omitempty"`  // send_message: base64 attachments captured at queue time (survive until approval)
	Videos    []string  `json:"videos,omitempty"`  // send_message: base64 video and audio, delivered as files, not images
	Requested time.Time `json:"requested"`
	// FromAgent is the agent that requested this delegation, captured at queue
	// time. Approving it runs the delegation with the same channel reach a
	// PRE-AUTHORIZED delegation gets — without this, whether a delegate could
	// address its delegator's channels would depend on whether the user had
	// pre-authorized the target, which is not a distinction the user is making
	// when they click Approve. Empty on legacy records (runs with its own scope).
	FromAgent string `json:"from_agent,omitempty"`
	// FromSession, FromChatID and FromHandle are the conversation a queued
	// delegation was asked from, captured at queue time, so an approved run
	// reports back into it and wakes the agent that asked. Without them the
	// run finished into the TARGET agent's own thread, where nobody was
	// waiting, and the asking agent went on telling the user it was still
	// pending. Empty when nothing was watching and on legacy records.
	FromSession string `json:"from_session,omitempty"`
	FromChatID  string `json:"from_chat_id,omitempty"`
	FromHandle  string `json:"from_handle,omitempty"`
	// FromThreadAgent is the agent whose conversation FromSession is, when
	// that is not FromAgent. An agent another one sent asks from inside its
	// sender's thread: FromAgent (the asker) is who the grants are for, and
	// this is who the result wakes. Woken as the asker, it landed in a thread
	// of that agent's that nobody was reading, and never came back.
	FromThreadAgent string `json:"from_thread_agent,omitempty"`
	// FromPrivate records that the asking conversation was running Private
	// (network cut off). An approved delegation runs Private too: approving the
	// work is not lifting the privacy it was asked under. A pre-authorized
	// delegation inherits it from the live turn; a queued one has no live turn
	// left to inherit from, so it is carried here.
	FromPrivate bool `json:"from_private,omitempty"`
	// BindReply makes a send_message approval also bind the recipient's 1:1
	// thread to the agent that asked (FromAgent), so it can read the reply:
	// one approval for "send this and read the answer", where it used to take
	// a send and a separate bind_thread request.
	BindReply bool `json:"bind_reply,omitempty"`
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

// preauthKey is the ALL-AGENTS key for a delegation target, and it keeps the
// shape the table has always had: a grant made before delegation was scoped
// says "any agent of mine may hand work to this one", which is what it meant.
func preauthKey(owner, agent string) string { return owner + ":" + agent }

// delegationScopeKey is the per-REQUESTER key: which agent is doing the
// delegating, not which one is being delegated to.
//
// The same reasoning as contacts, one layer in. An agent that may hand work to
// the agent that files invoices is a decision about that pairing; a grant the
// whole fleet shares is one any agent can spend, and the careful routing the
// owner set up holds nothing.
//
// A block written WITHOUT a scope still means what it has always meant, and the
// dispatch surfaces still read it that way: off limits to everyone, whatever
// route is taken to reach it.
func delegationScopeKey(owner, from, target string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return preauthKey(owner, target)
	}
	return owner + ":" + from + "\x00" + strings.TrimSpace(target)
}

// DelegationDecision is the decision recorded for exactly this scope (from ""
// is every agent), and whether there is one. Unlike DelegationPolicy it falls
// back neither to the all-agents row nor to the ask default, so a caller that
// reads one agent under several keys (its id, then the name a grant written
// before grants were keyed by id carries) orders the lookups itself, and can
// tell "never decided" from a decision to ask.
func DelegationDecision(db Database, owner, from, agent string) (string, bool) {
	if db == nil {
		return "", false
	}
	return storedDelegationPolicy(db, delegationScopeKey(owner, from, agent))
}

// SetDelegationPreAuthorized grants or clears the legacy allow flag for one
// scope. from "" is every agent.
func SetDelegationPreAuthorized(db Database, owner, from, agent string, on bool) {
	if db == nil {
		return
	}
	if on {
		db.Set(delegationPreauthTable, delegationScopeKey(owner, from, agent), true)
	} else {
		db.Unset(delegationPreauthTable, delegationScopeKey(owner, from, agent))
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

// contactPreauthKey is the ALL-AGENTS key, and it is deliberately the shape the
// table has always had: every grant written before contacts were scoped reads
// as "any agent of mine may message this person", which is exactly what it
// meant when it was made. So there is no migration, and nothing an owner has
// working today stops working.
func contactPreauthKey(owner, handle string) string { return owner + ":" + strings.TrimSpace(handle) }

// contactScopeKey is the per-agent key. The agent id sits between the owner and
// the handle, separated by a byte an id cannot contain, matching autoToolKey's
// shape in the app.
//
// Scoping exists because a permission and a guardrail have to share a scope or
// the tighter one is decorative: an agent carries a persona, a tone, and its own
// rules about what it may say to a person, and if the PERMISSION to reach that
// person belongs to the owner instead of the agent, then handing the message to
// a different agent walks around all of it. That is not even a bypass anybody
// has to intend; it is an ordinary dispatch.
func contactScopeKey(owner, agent, handle string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return contactPreauthKey(owner, handle)
	}
	return owner + ":" + agent + "\x00" + strings.TrimSpace(handle)
}

// IsContactPreAuthorized reports whether the user has granted standing
// authorization for the Operator to message this contact handle via phantom
// (set by "Always allow" on a send_message / converse_contact approval). When
// true, both one-shot texts and autonomous conversations to this handle run
// immediately instead of queuing. Scope is PER CONTACT — a new handle still
// queues. (The grant is shared across the two messaging actions by design.)
func IsContactPreAuthorized(db Database, owner, agent, handle string) bool {
	return ContactPolicy(db, owner, agent, handle) == PolicyAllow
}

// SetContactPreAuthorized grants or clears the legacy allow flag for one scope.
// agent "" is the all-agents scope.
func SetContactPreAuthorized(db Database, owner, agent, handle string, on bool) {
	if db == nil || owner == "" || strings.TrimSpace(handle) == "" {
		return
	}
	if on {
		db.Set(contactPreauthTable, contactScopeKey(owner, agent, handle), true)
	} else {
		db.Unset(contactPreauthTable, contactScopeKey(owner, agent, handle))
	}
}

// PolicyEntry pairs a permission subject (agent id or contact handle) with its
// current policy, for the Permissions settings list.
type PolicyEntry struct {
	Target string
	Policy string
	// Scope is the agent this decision is FOR: the one messaging the contact,
	// or the one doing the delegating. "" is every agent.
	Scope string
}

func normPolicy(p string) string {
	if p == PolicyAllow || p == PolicyAsk || p == PolicyBlock {
		return p
	}
	return PolicyAsk
}

// DelegationPolicy answers whether agent `from` may hand work to `agent`.
//
// The requester's own decision wins in both directions; where it has none, the
// all-agents row answers, and where that is absent the default is ask. Same
// precedence as contacts, and for the same reason: a promoted decision must not
// silently overrule one the owner already made about a particular agent.
//
// from "" asks the all-agents question directly, which is what the dispatch
// surfaces that only know a TARGET still do, and what the settings page does
// when it renders the promoted row.
func DelegationPolicy(db Database, owner, from, agent string) string {
	if db == nil {
		return PolicyAsk
	}
	if from = strings.TrimSpace(from); from != "" {
		if p, ok := storedDelegationPolicy(db, delegationScopeKey(owner, from, agent)); ok {
			return p
		}
	}
	if p, ok := storedDelegationPolicy(db, preauthKey(owner, agent)); ok {
		return p
	}
	return PolicyAsk
}

// SetDelegationPolicy records the standing policy and keeps the legacy preauth
// flag in sync (so ListDelegationPreAuthorizations / DelegationDecision
// stay correct without every caller learning about policies).
func SetDelegationPolicy(db Database, owner, from, agent, policy string) {
	if db == nil || strings.TrimSpace(agent) == "" {
		return
	}
	policy = normPolicy(policy)
	key := delegationScopeKey(owner, from, agent)
	db.Set(delegationPolicyTable, key, policy)
	if policy == PolicyAllow {
		db.Set(delegationPreauthTable, key, true)
	} else {
		db.Unset(delegationPreauthTable, key)
	}
}

// RemoveDelegationPolicy forgets this agent entirely — back to the ask default
// with no record, so it drops out of the Permissions list.
func RemoveDelegationPolicy(db Database, owner, from, agent string) {
	if db == nil {
		return
	}
	key := delegationScopeKey(owner, from, agent)
	db.Unset(delegationPolicyTable, key)
	db.Unset(delegationPreauthTable, key)
}

// IsDelegationBlocked reports whether delegations to this agent are blocked
// (auto-deny at the gate).
func IsDelegationBlocked(db Database, owner, from, agent string) bool {
	return DelegationPolicy(db, owner, from, agent) == PolicyBlock
}

// ListDelegationPolicies returns every agent with an explicit policy record,
// unioned with legacy allow grants, sorted by target.
func ListDelegationPolicies(db Database, owner string) []PolicyEntry {
	return listPolicies(db, delegationPolicyTable, owner, ListDelegationPreAuthorizations(db, owner))
}

// ContactPolicy / SetContactPolicy / RemoveContactPolicy / IsContactBlocked /
// ListContactPolicies mirror the delegation versions for phantom messaging.
// ContactPolicy answers whether THIS agent may message this person.
//
// The agent's own decision wins, in both directions: it can be blocked from a
// contact the owner promoted to every agent, and allowed one nobody else is.
// An all-agents row is the answer where the agent has none of its own, never
// an override of one — promoting a contact must not silently change an agent
// the owner has already thought about.
//
// agent "" asks the all-agents question directly, which is what the settings
// page does when it renders the promoted row.
func ContactPolicy(db Database, owner, agent, handle string) string {
	if db == nil || strings.TrimSpace(handle) == "" {
		return PolicyAsk
	}
	if agent = strings.TrimSpace(agent); agent != "" {
		if p, ok := storedContactPolicy(db, contactScopeKey(owner, agent, handle)); ok {
			return p
		}
	}
	if p, ok := storedContactPolicy(db, contactPreauthKey(owner, handle)); ok {
		return p
	}
	return PolicyAsk
}

// storedPolicy reads one key, explicit record first and the legacy allow flag
// second, and says whether anything was there at all. The distinction is what
// lets the caller fall through to the next scope instead of stopping at a
// default that was never written down.
func storedPolicy(db Database, policyTable, preauthTable, key string) (string, bool) {
	var p string
	if db.Get(policyTable, key, &p) && p != "" {
		return normPolicy(p), true
	}
	var on bool
	if db.Get(preauthTable, key, &on) && on {
		return PolicyAllow, true
	}
	return "", false
}

func storedContactPolicy(db Database, key string) (string, bool) {
	return storedPolicy(db, contactPolicyTable, contactPreauthTable, key)
}

func storedDelegationPolicy(db Database, key string) (string, bool) {
	return storedPolicy(db, delegationPolicyTable, delegationPreauthTable, key)
}

// SetContactPolicy records a decision for one scope. agent "" is all agents.
func SetContactPolicy(db Database, owner, agent, handle, policy string) {
	if db == nil || strings.TrimSpace(handle) == "" {
		return
	}
	policy = normPolicy(policy)
	key := contactScopeKey(owner, agent, handle)
	db.Set(contactPolicyTable, key, policy)
	if policy == PolicyAllow {
		db.Set(contactPreauthTable, key, true)
	} else {
		db.Unset(contactPreauthTable, key)
	}
}

// RemoveContactPolicy forgets one scope's decision. Removing an AGENT's row
// drops it back to whatever the all-agents answer is, which may be nothing.
func RemoveContactPolicy(db Database, owner, agent, handle string) {
	if db == nil {
		return
	}
	key := contactScopeKey(owner, agent, handle)
	db.Unset(contactPolicyTable, key)
	db.Unset(contactPreauthTable, key)
}

func IsContactBlocked(db Database, owner, agent, handle string) bool {
	return ContactPolicy(db, owner, agent, handle) == PolicyBlock
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
		// A scoped key carries its agent ahead of the target. An unscoped one
		// has no separator and reports no scope, which is how every row written
		// before contacts were scoped reads, and what it has always meant.
		scope, target := "", t
		if a, h, ok := strings.Cut(t, "\x00"); ok {
			scope, target = a, h
		}
		out = append(out, PolicyEntry{Target: target, Policy: p, Scope: scope})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Scope < out[j].Scope // all-agents first, then per agent
	})
	return out
}

// RunDelegation executes a delegation synchronously via the registered standing
// runner (the agent-execution closure) and records it to the run-ledger. It
// reuses the deny-by-default confirm posture — a delegated run never auto-
// approves high-consequence tools.
//
// via names the agent that delegated (and whoever delegated to it), so the
// delegate can reach that agent's channels instead of having to hand its output
// back up to be relayed. Optional: a delegation with no known delegator (an
// approval resolved on a legacy queued record) simply runs with its own scope.
func RunDelegation(ctx context.Context, db Database, owner, agent, brief string, via ...string) RunRecord {
	return executeStandingRun(ctx, db, StandingAgent{
		Owner: owner, Name: agent, AgentID: agent, Mission: brief,
		DispatchedBy: trimIDs(via),
	}, "delegation")
}

// trimIDs drops blanks from a dispatch chain so an unset caller id never
// becomes an empty seed in the channel-scope walk.
func trimIDs(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// --- Outbound writes --------------------------------------------------------
//
// Outbound writes: a request that changes something (POST, PUT, PATCH,
// DELETE) sent to a host none of the owner's credentials cover.
//
// A write to an API the owner set up goes through that API's credential,
// which has its own "ask before writes" setting. What is left is a write to a
// host nobody registered: a webhook, a public form, or a model sending data
// somewhere on its own. Nothing about the call says which, so it asks.
//
// The answer is kept per OWNER and per host, never per agent. A tool tried in
// Builder and then handed to a scheduled agent must keep working, which it
// would not if the trust stayed with the agent that happened to be asked.
//
// Reads pass. Only a call that changes something is held, and only on the
// paths where gohort sees the method: the fetch_url tool and the fetch hook a
// sandboxed script calls (its curl and wget stand-ins included).

// writeHostPolicyTable holds the owner's decision per host: <owner>:<host> ->
// allow | ask | block. No entry is ask.
const writeHostPolicyTable = "write_host_policy"

// OutboundWrite is one write waiting on a decision.
type OutboundWrite struct {
	// Owner is whose decision it is: their trusted and blocked hosts apply,
	// and Always allow records for them.
	Owner  string
	Method string // upper case
	URL    string
	Host   string // as the decision is keyed (writeHostOf)
	// CanAsk is false for a script, which cannot wait for an answer.
	CanAsk bool
	// Tunnel is an HTTPS connection through a run's proxy (Method CONNECT):
	// gohort sees the host, never the method, so it may carry a read or an
	// upload alike, and is decided by host. Package registries pass.
	Tunnel bool
}

// tunnelRegistryHosts are the hosts a proxy tunnel reaches without the
// owner's decision: package installers' (pip, npm, go, cargo, gem) and the
// GitHub download hosts they fetch from. Exact names only, never a suffix,
// so a bucket under a cloud provider's domain stays the owner's to decide.
// The owner blocking one still wins.
var tunnelRegistryHosts = map[string]bool{
	"pypi.org":                             true,
	"files.pythonhosted.org":               true,
	"registry.npmjs.org":                   true,
	"registry.yarnpkg.com":                 true,
	"proxy.golang.org":                     true,
	"sum.golang.org":                       true,
	"index.crates.io":                      true,
	"static.crates.io":                     true,
	"rubygems.org":                         true,
	"github.com":                           true,
	"codeload.github.com":                  true,
	"objects.githubusercontent.com":        true,
	"raw.githubusercontent.com":            true,
	"release-assets.githubusercontent.com": true,
}

// Summary names the call for a person: "a POST to host", or for a tunnel
// "an HTTPS connection to host through the proxy".
func (w OutboundWrite) Summary() string {
	if w.Tunnel {
		return "an HTTPS connection to " + w.Host + " through the proxy"
	}
	return fmt.Sprintf("a %s to %s", w.Method, w.Host)
}

// writeHostOf is the host a write to rawURL goes to, as the decision is
// keyed: lower case, no port, no trailing dot. "" when the URL has no host.
func writeHostOf(rawURL string) string {
	u, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

func writeHostKey(owner, host string) string {
	return owner + ":" + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// writeHostPolicy is the owner's decision about writes to host: PolicyAllow,
// PolicyBlock, or PolicyAsk when they have not decided.
func writeHostPolicy(db Database, owner, host string) string {
	if db == nil || owner == "" || strings.TrimSpace(host) == "" {
		return PolicyAsk
	}
	var p string
	if db.Get(writeHostPolicyTable, writeHostKey(owner, host), &p) {
		return normPolicy(p)
	}
	return PolicyAsk
}

// adminWriteHostTable holds the administrator's decisions about sending to a
// site, for every user: <host> -> allow | block. Read by Decided, around the
// owner's own: a block here wins over everything, an allow here gives way to
// an owner's block.
const adminWriteHostTable = "write_host_policy_admin"

// AdminSites is the administrator's site list. One name with methods, so the
// list adds one word to the namespace of every file that dot-imports core.
var AdminSites adminSiteList

type adminSiteList struct{}

// Policy is the administrator's decision about host: PolicyAllow,
// PolicyBlock, or "" when they have made none.
func (adminSiteList) Policy(db Database, host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if db == nil || host == "" {
		return ""
	}
	var p string
	if !db.Get(adminWriteHostTable, host, &p) {
		return ""
	}
	switch p = normPolicy(p); p {
	case PolicyAllow, PolicyBlock:
		return p
	}
	return ""
}

// Set records the administrator's decision about host for every user. Only
// allow and block are decisions at this level; anything else, empty
// included, removes it, so the owners' own decisions apply.
func (adminSiteList) Set(db Database, host, policy string) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if db == nil || host == "" {
		return
	}
	if p := normPolicy(policy); p == PolicyAllow || p == PolicyBlock {
		db.Set(adminWriteHostTable, host, p)
		return
	}
	db.Unset(adminWriteHostTable, host)
}

// List is every site the administrator decided about, sorted by host.
func (l adminSiteList) List(db Database) []PolicyEntry {
	if db == nil {
		return nil
	}
	var out []PolicyEntry
	for _, k := range db.Keys(adminWriteHostTable) {
		if p := l.Policy(db, k); p != "" {
			out = append(out, PolicyEntry{Target: k, Policy: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// SiteFromInput is the site a typed address names, keyed as the write check
// keys it (lower case, no port, path or trailing dot), or why it cannot be one.
// A full link is accepted, since that is what people have to hand.
func SiteFromInput(in string) (host, why string) {
	in = strings.TrimSpace(in)
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := neturl.Parse(in)
	if err != nil {
		return "", "that is not a site address"
	}
	host = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch {
	case host == "" || strings.ContainsAny(host, " *"):
		return "", "name one site, like hooks.example.com"
	case !strings.Contains(host, ".") && net.ParseIP(host) == nil:
		return "", host + " is not a public site: a site needs a domain, like hooks.example.com"
	case IsNonPublicHost(host):
		return "", host + " is a private address, which nothing here sends to without an API credential naming it"
	}
	return host, ""
}

// SetWriteHostPolicy records the owner's decision about writes to host. An
// empty policy forgets it, so writes there ask again.
func SetWriteHostPolicy(db Database, owner, host, policy string) {
	if db == nil || owner == "" || strings.TrimSpace(host) == "" {
		return
	}
	if strings.TrimSpace(policy) == "" {
		db.Unset(writeHostPolicyTable, writeHostKey(owner, host))
		return
	}
	db.Set(writeHostPolicyTable, writeHostKey(owner, host), normPolicy(policy))
}

// ListWriteHostPolicies is every host the owner has decided about, sorted.
func ListWriteHostPolicies(db Database, owner string) []PolicyEntry {
	return listPolicies(db, writeHostPolicyTable, owner, nil)
}

// OutboundWriteRefusal is "" when this request may go out, or the refusal to
// return instead. Call it after a credential has had the chance to claim the
// URL: a covered host is the credential's to govern, and never reaches here.
//
// canAsk says whether the caller can wait for a person's answer. The fetch_url
// tool can; a script calling the fetch hook cannot, so it is refused with what
// to do instead.
//
// The session's ApproveWrite decides when the app set one: it knows whose
// decision it is (an agent's owner, not whoever is running it), who is
// watching, and where an unanswered one is queued. Otherwise the session
// user's own decision applies, and an undecided host gets a plain question
// card, or is refused when there is nobody to ask.
//
// A method so a nil session works, and so it adds no name to every file that
// dot-imports core.
func (sess *ToolSession) OutboundWriteRefusal(method, rawURL string, canAsk bool) string {
	if !IsWriteMethod(method) {
		return ""
	}
	w := OutboundWrite{Method: strings.ToUpper(strings.TrimSpace(method)), URL: rawURL, Host: writeHostOf(rawURL), CanAsk: canAsk}
	if w.Host == "" {
		return ""
	}
	if sess != nil && sess.ApproveWrite != nil {
		return sess.ApproveWrite(w)
	}
	var ask func(prompt, detail string, yes []string) string
	if sess != nil {
		w.Owner, ask = sess.Username, sess.AskInChat
	}
	if refusal, decided := w.Decided(); decided {
		return refusal
	}
	return w.Ask(ask, true)
}

// OutboundTunnelRefusal is "" when the run's proxy may open an HTTPS tunnel
// to host, or the refusal to answer the CONNECT with. A tunnel can carry an
// upload as easily as a download, and gohort cannot see which, so it goes
// only to a package registry or a site the owner has trusted for writes; any
// other host is refused, and on an unattended run queued for the owner the
// way a write is (approving trusts the host). A proxy client cannot wait for
// a card, so nobody is asked.
func (sess *ToolSession) OutboundTunnelRefusal(host string) string {
	w := OutboundWrite{Method: "CONNECT", Host: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "."), Tunnel: true}
	if w.Host == "" {
		return "NOT CONNECTED: the proxy needs a host to connect to."
	}
	w.URL = net.JoinHostPort(w.Host, "443")
	if sess != nil && sess.ApproveWrite != nil {
		return sess.ApproveWrite(w)
	}
	if sess != nil {
		w.Owner = sess.Username
	}
	if refusal, decided := w.Decided(); decided {
		return refusal
	}
	return w.Ask(nil, false)
}

// Decided applies the standing decisions: decided is true when the host is
// trusted (refusal "") or blocked (refusal says so), false when it is still
// the owner's to answer. In order:
//
//   - the administrator's Never, for every user, over everything below;
//   - the owner's own Never, then their Always allow;
//   - the administrator's Always allow;
//   - for a tunnel, a package registry.
func (w OutboundWrite) Decided() (refusal string, decided bool) {
	if AdminSites.Policy(RootDB, w.Host) == PolicyBlock {
		Log("[outbound] %s %s: blocked by the administrator, refused", w.Method, w.Host)
		if w.Tunnel {
			return fmt.Sprintf("NOT CONNECTED: the administrator has blocked sending to %s, for every user. Do not reach it another way.", w.Host), true
		}
		return fmt.Sprintf("NOT SENT: the administrator has blocked sending to %s, for every user. Do not send it another way.", w.Host), true
	}
	switch writeHostPolicy(RootDB, w.Owner, w.Host) {
	case PolicyAllow:
		Log("[outbound] %s %s: trusted by %s, sent", w.Method, w.Host, w.Owner)
		return "", true
	case PolicyBlock:
		Log("[outbound] %s %s: blocked by %s, refused", w.Method, w.Host, w.Owner)
		if w.Tunnel {
			return fmt.Sprintf("NOT CONNECTED: the owner has blocked sending to %s. Do not reach it another way.", w.Host), true
		}
		return fmt.Sprintf("NOT SENT: the owner has blocked sending changes to %s. Do not send it another way.", w.Host), true
	}
	if AdminSites.Policy(RootDB, w.Host) == PolicyAllow {
		Log("[outbound] %s %s: trusted by the administrator, sent", w.Method, w.Host)
		return "", true
	}
	if w.Tunnel && tunnelRegistryHosts[w.Host] {
		return "", true
	}
	return "", false
}

// Ask puts the write to the person on ask's card: Allow once, Always allow
// <host> when offerAlways (it trusts the host for the owner), or Deny. ""
// when they allowed it, the refusal otherwise. A script (CanAsk false) is
// refused without a card, and pointed at fetch_url, which can ask; nil ask
// means nobody is watching, and is refused too.
func (w OutboundWrite) Ask(ask func(prompt, detail string, yes []string) string, offerAlways bool) string {
	if w.Tunnel {
		Log("[outbound] %s %s: tunnel to a host not trusted, refused", w.Method, w.Host)
		return fmt.Sprintf("NOT CONNECTED: the proxy opens HTTPS connections only to package registries and to sites the owner has trusted, and %s is neither: a connection through it could carry an upload as easily as a download, and gohort cannot see which. To read from %s, use curl or wget here, or the fetch_url tool; to send to it, use fetch_url, which asks the owner first. The owner can also add %s under Network on the agent's Security page.", w.Host, w.Host, w.Host)
	}
	if !w.CanAsk {
		Log("[outbound] %s %s: a script cannot ask, refused", w.Method, w.Host)
		return fmt.Sprintf("NOT SENT: a %s to %s sends data to a host the owner has not trusted, and a script cannot stop to ask them. Make this one request with the fetch_url tool instead, which asks the owner first, or ask them to set %s to Always allow on the Permissions page.", w.Method, w.Host, w.Host)
	}
	if ask == nil {
		Log("[outbound] %s %s: not trusted and nobody to ask, refused", w.Method, w.Host)
		return fmt.Sprintf("NOT SENT: a %s to %s needs the owner's approval: no credential covers that host and they have not trusted it, and nobody is watching this run to ask. Do not send it another way; tell them what you meant to send.", w.Method, w.Host)
	}
	detail := fmt.Sprintf("%s %s\n\nThis sends data to a host none of the owner's API credentials cover, and it has not been trusted.", w.Method, w.URL)
	yes := []string{"Allow once"}
	always := "Always allow " + w.Host
	if offerAlways {
		yes = append(yes, always)
		detail += " Always allow trusts the host for every agent you own; you can take that back on the Permissions page."
	}
	// The host ends the line with no "?" after it, which would read as part
	// of the address.
	switch ask(fmt.Sprintf("Send a %s request to this site: %s", w.Method, w.Host), detail, yes) {
	case "":
		Log("[outbound] %s %s: declined", w.Method, w.Host)
		return fmt.Sprintf("NOT SENT: the owner declined this %s to %s. Do not send it another way.", w.Method, w.Host)
	case always:
		SetWriteHostPolicy(RootDB, w.Owner, w.Host, PolicyAllow)
		Log("[outbound] %s %s: %s trusted the host", w.Method, w.Host, w.Owner)
	default:
		Log("[outbound] %s %s: allowed once", w.Method, w.Host)
	}
	return ""
}
