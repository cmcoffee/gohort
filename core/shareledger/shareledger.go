// Package shareledger answers two questions no single record can: what have I
// shared, and what do I have because somebody shared it with me.
//
// Both are cross-kind by nature. An owner who wants to audit or take back what
// they have handed out currently walks every surface that can share anything —
// agents, skills, collections, tools, credentials, pipelines, machines — and
// has to remember which door a given grant went through. A recipient asking
// "what do I have that isn't mine" gets a different answer in each of four
// places. Neither question belongs on any one thing's page, because neither
// question is about any one thing.
//
// The registry is how that gets answered without this package knowing a single
// kind. Each one registers three functions: what its owner has shared, what has
// been shared with a user, and how to take one back. Nothing here knows what an
// agent is, what a credential means, or which of them may reach a whole
// deployment; it collects, sorts and hands back rows. A kind added next year
// gets both views by registering, and this file does not change.
//
// A leaf package rather than a file in core for the reason core/peershare and
// core/notices are: core sits at its export ceiling, and every exported name
// there lands in the namespace of every file that dot-imports it.
//
// NOT a second grant model. Every provider reads the recipient list that
// already lives on its own record and revokes through the setter that already
// owns it, so there is exactly one place a share is stored and one rule about
// what it means. This is a view over those, plus a way to reach their revoke.
package shareledger

import (
	"sort"
	"strings"
	"sync"
)

// Grant is one shared thing, as either side sees it.
type Grant struct {
	Kind  string `json:"kind"`  // registry key: "agent", "skill", …
	Label string `json:"label"` // what to call that kind on screen
	ID    string `json:"id"`
	Name  string `json:"name"`
	// Owner is who shared it; Recipients is who has it. A row in the
	// owner's view fills Recipients; one in a recipient's view fills Owner.
	Owner      string   `json:"owner,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
	// Reach says how far this goes in that kind's own words — "Shared with
	// bob", "Deployment-wide", "Reads only". Left to the provider because
	// what a grant ALLOWS differs per kind and flattening it to a count
	// would lose the only part worth reading.
	Reach string `json:"reach,omitempty"`
	// Wide marks a grant that reaches everybody. Listed separately from
	// Reach because "shared with two people" and "shared with everybody"
	// are different decisions and should never render the same.
	Wide bool `json:"wide,omitempty"`
	// Detail is an optional line about what the recipient has to supply
	// themselves, or what did not travel. The manifest half.
	Detail string `json:"detail,omitempty"`
	// Revocable is false for a grant this ledger cannot take back — one
	// an administrator granted, or one whose kind offers no revoke. A
	// button that cannot work is worse than an absent one.
	Revocable bool `json:"revocable"`
}

// Provider is one kind's backend. A kind with nothing to offer on a side
// leaves that function nil rather than returning an empty slice, so "this kind
// has no recipient view" and "this user has nothing" stay distinguishable.
type Provider struct {
	// Label names the kind on screen: "Agent", "Knowledge", "Credential".
	Label string
	// Mine lists what owner has shared with anybody.
	Mine func(owner string) []Grant
	// ToMe lists what has been shared WITH user by anybody else.
	ToMe func(user string) []Grant
	// Revoke drops one recipient from one record. An empty recipient means
	// every recipient. Returning an error leaves the row alone and says why.
	Revoke func(owner, id, recipient string) error
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// Register installs a kind's backend. Called once at startup by the package
// that owns the records; a later registration for the same kind replaces it.
func Register(kind string, p Provider) {
	kind = strings.TrimSpace(kind)
	if kind == "" || p.Label == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	providers[kind] = p
}

// Mine is everything this owner has shared, across every registered kind.
func Mine(owner string) []Grant {
	return collect(owner, func(p Provider) func(string) []Grant { return p.Mine })
}

// ToMe is everything shared WITH this user, across every registered kind.
func ToMe(user string) []Grant {
	return collect(user, func(p Provider) func(string) []Grant { return p.ToMe })
}

func collect(who string, pick func(Provider) func(string) []Grant) []Grant {
	out := []Grant{}
	if strings.TrimSpace(who) == "" {
		return out
	}
	mu.RLock()
	snapshot := make(map[string]Provider, len(providers))
	for k, p := range providers {
		snapshot[k] = p
	}
	mu.RUnlock()
	for kind, p := range snapshot {
		fn := pick(p)
		if fn == nil {
			continue
		}
		for _, g := range fn(who) {
			// The registry stamps kind and label rather than trusting each
			// provider to repeat them: a row that disagrees with the key it
			// was registered under is a row nothing can route a revoke to.
			g.Kind, g.Label = kind, p.Label
			out = append(out, g)
		}
	}
	// By kind, then name: the same two shares list in the same order twice,
	// and everything of one kind reads together.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Revoke routes a take-back to the kind that owns the record.
func Revoke(kind, owner, id, recipient string) error {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok || p.Revoke == nil {
		return errString("nothing here can take back a " + kind + " share")
	}
	return p.Revoke(owner, id, recipient)
}

// Kinds returns the registered kind keys, for a caller that wants to say what
// this deployment can share at all.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type errString string

func (e errString) Error() string { return string(e) }
