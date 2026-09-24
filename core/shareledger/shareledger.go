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
	"strconv"
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
	// Dependents is who relies on this today, and through what: per person,
	// the names of their own things that reference it. Filled on the owner's
	// side, because the owner is the one about to take it away, and "they
	// lose it" reads very differently from "their Triage agent stops working".
	Dependents []Dependent `json:"dependents,omitempty"`
}

// Dependent is one person whose own things reference a shared record.
type Dependent struct {
	User string `json:"user"`
	// Uses names their things that reference it, in their words (an agent's
	// name, not its id).
	Uses []string `json:"uses"`
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
	// Carries is what comes WITH this record for whoever holds it: the tools
	// it runs, the documents it reads, the behaviour it applies. The other
	// half of Manifest, and the half the recipient has no other way to see.
	//
	// Somebody handed an agent is about to run its author's code against its
	// author's documents. That they cannot reach any of it OUTSIDE the agent
	// is what makes the arrangement safe; it is not a reason to leave them
	// guessing about what happens inside it.
	Carries func(owner, id, viewer string) []string
	// Manifest is what THIS recipient still has to supply for this record to
	// do what it says — a credential of their own by the right name, a tool
	// they have not taken yet. Empty when nothing is needed.
	//
	// Per recipient, not per share, because the answer differs by person:
	// one colleague already has a key of that name and another does not, and
	// a single line written at share time would be wrong for one of them the
	// moment either changes.
	Manifest func(owner, id, recipient string) []string
	// Dependents narrows who can depend on this record before FindDependents
	// is asked about them. Nil for a kind where holding it is enough (a
	// skill or collection id on an agent names exactly one record); set by a
	// kind that is referenced some other way, where holding the grant and
	// having taken it are different things - a tool is referenced by NAME, so
	// only the people who took this owner's copy can be relying on it.
	Dependents func(owner, id string, users []string) []Dependent
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
	return collect(owner, true, func(p Provider) func(string) []Grant { return p.Mine })
}

// ToMe is everything shared WITH this user, across every registered kind.
func ToMe(user string) []Grant {
	return collect(user, false, func(p Provider) func(string) []Grant { return p.ToMe })
}

// collect gathers one side across every kind. withDependents asks, per grant,
// who relies on it; only the owner's view does:
// that is the side deciding whether to take something back, and "they lose it"
// reads very differently from "their Triage agent stops working".
func collect(who string, withDependents bool, pick func(Provider) func(string) []Grant) []Grant {
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
			if withDependents && g.Dependents == nil && (g.Wide || len(g.Recipients) > 0) {
				// Recipients when there are some; nobody-in-particular (nil)
				// for a grant that reaches everybody.
				var users []string
				if !g.Wide {
					users = g.Recipients
				}
				g.Dependents = dependentsWith(p, kind, who, g.ID, users)
			}
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

// FindDependents, when wired, answers which of these users' own things
// reference kind/id: agents that name a skill, attach a collection, load a
// tool. Nil users means anybody at all, for a record that reached everybody.
//
// A hook for the reason NotifyRecipient is one: what references a record is
// the business of whatever app owns the referencing things, and this package
// knows no kind on either end. Unwired, nobody depends on anything and the
// ledger reads as it did before.
var FindDependents func(kind, owner, id string, users []string) []Dependent

// OnWithdrawn, when wired, is told each time a record stops reaching people,
// with its last name. Whatever holds a reference to it by id is about to be
// left with only the id, and this is the last moment the name is known.
var OnWithdrawn func(kind, owner, id, name string, dependents []Dependent)

// DependentsOf is who relies on this record today, among users (nil for
// anybody), asked of the kind first when it narrows the question.
func DependentsOf(kind, owner, id string, users []string) []Dependent {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok {
		return nil
	}
	return dependentsWith(p, kind, owner, id, users)
}

func dependentsWith(p Provider, kind, owner, id string, users []string) []Dependent {
	if p.Dependents != nil {
		return p.Dependents(owner, id, users)
	}
	if FindDependents == nil {
		return nil
	}
	return FindDependents(kind, owner, id, users)
}

// Withdrawn tells each of lost that a record they had no longer reaches them,
// and names which of their own things relied on it.
//
// Called by the kind at the moment access is taken away - a share revoked, a
// publication taken back, the record deleted - on whichever path did it. The
// share already told them it arrived; a take-back that told them nothing is
// how somebody finds out by watching their agent answer without it.
func Withdrawn(kind, owner, id, name string, lost []string) {
	if len(lost) == 0 {
		return
	}
	withdrawn(kind, owner, id, name, lost, DependentsOf(kind, owner, id, lost))
}

// WithdrawnFromEverybody is Withdrawn for a record that reached everybody:
// told to the people who were actually relying on it, since telling the whole
// deployment that something most of them never used has gone would be noise
// that teaches everyone to ignore the rest.
func WithdrawnFromEverybody(kind, owner, id, name string) {
	deps := DependentsOf(kind, owner, id, nil)
	var lost []string
	for _, d := range deps {
		if len(d.Uses) > 0 {
			lost = append(lost, d.User)
		}
	}
	withdrawn(kind, owner, id, name, lost, deps)
}

func withdrawn(kind, owner, id, name string, lost []string, deps []Dependent) {
	if strings.TrimSpace(name) == "" {
		name = id
	}
	if OnWithdrawn != nil {
		OnWithdrawn(kind, owner, id, name, deps)
	}
	if NotifyRecipient == nil {
		return
	}
	uses := make(map[string][]string, len(deps))
	for _, d := range deps {
		uses[d.User] = d.Uses
	}
	what := "\"" + name + "\" (" + strings.ToLower(labelOf(kind)) + ")"
	for _, u := range lost {
		if u = strings.TrimSpace(u); u == "" || u == owner {
			continue
		}
		// A deployment record can have no owner (the framework minted it),
		// and "from " followed by nothing reads as a bug.
		from, ask := "", "or replace it."
		if owner != "" {
			from, ask = " from "+owner, "or ask "+owner+" to share it again."
		}
		intro := what + from + " is no longer available to you: it was taken back or deleted."
		var needs []string
		// Only a person with something relying on it has anything to do,
		// and the notice is filed as waiting on them exactly then.
		if names := uses[u]; len(names) > 0 {
			needs = append(needs, "Your "+plural(len(names), "agent", "agents")+" "+joinNames(names)+
				" "+plural(len(names), "uses", "use")+" it and now "+plural(len(names), "runs", "run")+
				" without it. Remove it there, "+ask)
		}
		NotifyRecipient(u, what+" is no longer available", intro, needs)
	}
}

// labelOf is what to call a kind on screen, falling back to its key.
func labelOf(kind string) string {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if ok && p.Label != "" {
		return p.Label
	}
	return kind
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// joinNames lists names for a sentence, capped so a person with forty agents
// on the default tool pool gets a readable line rather than all forty.
func joinNames(names []string) string {
	const show = 5
	quoted := make([]string, 0, len(names))
	for i, n := range names {
		if i == show {
			quoted = append(quoted, "and "+strconv.Itoa(len(names)-show)+" more")
			break
		}
		quoted = append(quoted, "\""+n+"\"")
	}
	return strings.Join(quoted, ", ")
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
	return collect(owner, false, func(p Provider) func(string) []Grant { return p.Candidates })
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
		intro := owner + " shared " + name + " with you."
		// The first line of what it carries, which by convention is the
		// summary sentence: whose things come with it and that they are
		// reachable through this and nowhere else. The full list is a click
		// away; the notice says enough to know what arrived.
		if c := Carries(kind, owner, id, u); len(c) > 0 {
			intro += " " + c[0]
		}
		NotifyRecipient(u, owner+" shared "+name+" with you", intro, Manifest(kind, owner, id, u))
	}
}

// Carries asks one kind what comes with this record for whoever holds it.
func Carries(kind, owner, id, viewer string) []string {
	mu.RLock()
	p, ok := providers[strings.TrimSpace(kind)]
	mu.RUnlock()
	if !ok || p.Carries == nil {
		return nil
	}
	return p.Carries(owner, id, viewer)
}
