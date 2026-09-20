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

	// Candidates lists what this owner could share, for the guided flow's
	// first step. Nil when the kind is not something somebody sets out to
	// share — a credential is handed over as part of sharing the thing that
	// uses it far more often than on its own.
	Candidates func(owner string) []Grant
	// Plan says what handing this record over would DECIDE, so the owner is
	// asked rather than assumed at. Nil or empty means the share is one
	// decision and the flow goes straight to confirming it.
	Plan func(owner, id string, recipients []string) []Decision
	// Share hands it over, with the answers to whatever Plan asked, and
	// returns one line per thing it did or could not do.
	Share func(owner, id string, recipients []string, answers map[string]string) []string
	// Manifest is what THIS recipient still has to supply for this record to
	// do what it says — a credential of their own by the right name, a tool
	// they have not taken yet. Empty when nothing is needed.
	//
	// Per recipient, not per share, because the answer differs by person:
	// one colleague already has a key of that name and another does not, and
	// a single line written at share time would be wrong for one of them the
	// moment either changes.
	Manifest func(owner, id, recipient string) []string
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

// ----------------------------------------------------------------------
// The guided route
// ----------------------------------------------------------------------

// Choice is one answer to a Decision.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// Help is the consequence, in one line. The whole reason a guided flow
	// exists is that these outcomes are not obvious from their names, and a
	// list of bare options would be the same assumption in a nicer wrapper.
	Help string `json:"help,omitempty"`
}

// Decision is something the owner has to answer before a share can go out.
//
// Kinds return their own, because what a share implies differs entirely
// between them: handing over an agent raises a question per credential it
// touches, handing over a skill raises none. A registry that tried to know
// which was which would be back to naming kinds.
type Decision struct {
	// Key is the form field this answers under, stable across a rerender.
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Intro   string   `json:"intro,omitempty"`
	Options []Choice `json:"options"`
	Default string   `json:"default,omitempty"`
}

// Options is everything this owner could share, across every kind. For the
// first step of the guided flow, which is a list nobody can assemble from one
// page.
func Options(owner string) []Grant {
	return collect(owner, func(p Provider) func(string) []Grant { return p.Candidates })
}

// Plan asks one kind what handing this record over would decide.
func Plan(kind, owner, id string, recipients []string) []Decision {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok || p.Plan == nil {
		return nil
	}
	return p.Plan(owner, id, recipients)
}

// Share hands the record over, with the answers to whatever Plan asked.
//
// Returns what it did AND what it could not, because a report listing only
// successes is how somebody concludes their team has a working thing while a
// piece of it is still missing.
func Share(kind, owner, id string, recipients []string, answers map[string]string) ([]string, error) {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok || p.Share == nil {
		return nil, errString("nothing here can share a " + kind)
	}
	lines := p.Share(owner, id, recipients, answers)
	tellRecipients(kind, owner, id, displayName(p, owner, id), recipients)
	return lines, nil
}

// displayName finds what to call this record, so a notice says "Troubleshooter"
// rather than quoting an id at somebody who has never seen it.
func displayName(p Provider, owner, id string) string {
	if p.Candidates != nil {
		for _, g := range p.Candidates(owner) {
			if g.ID == id {
				return g.Name
			}
		}
	}
	return id
}

// NotifyRecipient, when wired at startup, tells somebody that something has
// been shared with them and what they still need for it.
//
// A hook rather than a call into a notification store, so this package stays
// storage-free and the deployment decides what "tell them" means. Unwired, a
// share is silent — which is what it was before any of this.
// Needs is kept separate from the intro rather than folded into one blob,
// because whether there IS anything for the recipient to do decides how the
// notice should read — and a deployment that had to infer that from the length
// of a slice would infer it differently in two places.
var NotifyRecipient func(recipient, title, intro string, needs []string)

// Manifest asks one kind what this recipient still has to supply.
func Manifest(kind, owner, id, recipient string) []string {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok || p.Manifest == nil {
		return nil
	}
	return p.Manifest(owner, id, recipient)
}

// tellRecipients is the other half of a share, and the half that was missing.
//
// The owner gets a report of what they just did. The people on the other end
// got nothing at all — no word that anything arrived, and no word that the
// thing which arrived needs something from them before it works. A share that
// tells only the person who made it is how a colleague finds out by running
// something and watching it fail.
func tellRecipients(kind, owner, id, name string, recipients []string) {
	if NotifyRecipient == nil {
		return
	}
	for _, u := range recipients {
		NotifyRecipient(u, owner+" shared "+name+" with you",
			owner+" shared "+name+" with you.", Manifest(kind, owner, id, u))
	}
}
