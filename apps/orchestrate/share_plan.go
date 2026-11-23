package orchestrate

// What handing an agent over actually decides.
//
// "Share this with my team so we are all working from the same agent" is one
// request that resolves into several answers, and the fan-out used to make all
// of them silently: share every dependency it could, skip every credential,
// say so afterwards. Skipping is usually right for a credential — their calls
// should go out as them — but usually is not always, and the owner is the one
// who knows which this is.
//
// So the flow asks. One decision per thing the agent reaches that its
// recipients cannot, with the consequence of each answer on screen rather than
// in a document somebody reads afterwards.

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// The answers a decision can carry. Values rather than booleans because a
// credential has four, and a two-state control that grew a third option is how
// the third one ends up meaning "whatever the second one meant".
const (
	credOwn      = "own"      // they bring their own
	credRead     = "read"     // my account for reads, theirs for writes
	credWrite    = "write"    // share mine, writes included
	credWithhold = "withhold" // disallow: withheld for the people it is shared with
	shareSkip    = "skip"     // turn it off for the agent entirely (no longer offered)
)

// planAgentShare asks about everything the recipients cannot reach.
//
// Only the gaps. A dependency they already have raises no question, and a list
// that asked about all of them would bury the two that matter in eight that
// do not.
func planAgentShare(owner, id string, recipients []string) []shareledger.Decision {
	udb := UserDB(orchestrateBaseDB, owner)
	a, ok := loadAgent(udb, id)
	if !ok || a.Owner != owner {
		return nil
	}
	// Planned against the recipients being proposed, not against whoever the
	// agent already lists: the question is what THESE people would be missing.
	a.AllowedUsers = recipients
	a.Everyone = false

	// Credentials, and only credentials. Everything else an agent uses travels
	// with it and is scoped to it, so there is no question to ask: asking
	// "share this tool too?" had one possible answer, and the answer it
	// offered was the wrong shape anyway — handing the tool over, when the
	// agent only ever needed to run it.
	var out []shareledger.Decision
	for _, it := range agentReachOf(udb, owner, a).Items {
		if it.Gap {
			out = append(out, credentialDecision(it, owner))
		}
	}
	return out
}

// credentialDecision is the fork the fan-out used to make on everybody's
// behalf. Four answers, and the difference between them is whose name a call
// goes out under — which is not a thing to assume for somebody.
func credentialDecision(it reachItem, owner string) shareledger.Decision {
	// The lend options are offered only where the credential's own policy
	// allows them. A key its owner already decided never to lend should not
	// be one careless pass through a wizard away from being lent, and asking
	// again on every share is how that pass eventually happens.
	//
	// The flow only declines to OFFER. The refusal that matters is on the
	// setter, where every other door goes through too.
	lend, lendWrite := true, true
	if c, ok := Secure().LoadUser(owner, it.id); ok {
		lend, lendWrite = c.MayLend()
	}
	intro := "Whose account should the people you share this with use? They can always connect their own: once they do, every call to it goes out as them, reads included, and they see only what their own access allows, which may be less than yours."
	read := shareledger.Choice{Value: credRead, Label: "My account for reads, theirs for writes",
		Help: "Reads go through your key, so the agent works for them from the start, and they read with your access until they connect their own account. A write asks them to connect theirs first, so it goes out as them."}
	own := shareledger.Choice{Value: credOwn, Label: "They bring their own",
		Help: "The agent uses their own credential of this name. Until they have one, the tools that need it fail and say so."}
	write := shareledger.Choice{Value: credWrite, Label: "Share mine, writes included",
		Help: "Everything goes through your key until they connect their own, writes included: what they write arrives at the far end as YOU, and only gohort's record says who it was. For a service people have no account of their own on."}
	withhold := shareledger.Choice{Value: credWithhold, Label: "Disallow",
		Help: "The tools that use it are withheld for the people you share this with. You keep them."}
	var opts []shareledger.Choice
	def := credRead
	switch {
	case !lend:
		intro += " You have set this key to be lent to nobody, so your account is not offered. Change that on the credential itself if you meant to lend it."
		opts, def = []shareledger.Choice{own, withhold}, credOwn
	case !lendWrite:
		intro += " You have set this key to be lent for reads only, so sharing it for writes is not offered."
		opts = []shareledger.Choice{read, own, withhold}
	default:
		opts = []shareledger.Choice{read, own, write, withhold}
	}
	return shareledger.Decision{
		Key:     "cred:" + it.id,
		Title:   "Credential — " + it.Name,
		Intro:   intro,
		Options: opts,
		Default: def,
	}
}

// shareAgentGuided performs the share the answers describe.
//
// The recipient list goes on first: everything after it is a dependency
// decision, and a dependency shared to somebody who does not have the agent
// would be a grant nobody asked for if the run failed halfway.
func shareAgentGuided(owner, id string, recipients []string, answers map[string]string) []string {
	udb := UserDB(orchestrateBaseDB, owner)
	a, ok := loadAgent(udb, id)
	if !ok || a.Owner != owner {
		return []string{"That agent is not yours to share."}
	}
	a.AllowedUsers = mergeRecipients(a.AllowedUsers, recipients)
	if _, err := saveAgent(udb, a); err != nil {
		return []string{"Could not share the agent: " + err.Error()}
	}
	out := []string{"Agent " + a.Name + " → " + ShareRecipientsText(recipients)}

	// Everything else the agent uses travels with it, so there is nothing to
	// grant and nothing to report beyond saying so once.
	out = append(out, "Its tools, skills, documents and recipes travel with it, readable through this agent and nowhere else.")
	for _, it := range agentReachOf(udb, owner, a).Items {
		if it.Gap {
			out = append(out, applyCredentialAnswer(owner, id, it, recipients, answers["cred:"+it.id]))
		}
	}
	return out
}

// applyCredentialAnswer is the only place a key changes hands, and it reports
// every branch — including the one that does nothing, since "they bring their
// own" looks identical to "nothing happened" unless it is said.
func applyCredentialAnswer(owner, agentID string, it reachItem, recipients []string, answer string) string {
	switch answer {
	case credRead, credWrite:
		if _, ok := Secure().LoadUser(owner, it.id); !ok {
			return "Credential " + it.Name + ": not yours to lend, so they will need their own"
		}
		// Scoped to THIS agent. They needed the key to run this, so this is
		// where it works: not from an agent of their own, not from a tool they
		// write next week. The grant is as narrow as the reason for it.
		// To people. A key is lent to a person, so a team on the list is
		// its members today; someone who joins later is shown as missing
		// it, and someone who leaves has it taken back (reconcileShareLends).
		people := ShareMembers(recipients, owner)
		if len(people) == 0 {
			return "Credential " + it.Name + ": nobody on the list to lend it to"
		}
		if err := Secure().LendForAgent(owner, it.id, agentID, people, answer == credWrite); err != nil {
			return "Credential " + it.Name + ": " + err.Error()
		}
		// Recorded, so taking somebody off this agent takes the lend back
		// with them — and takes back nothing the owner lent by hand.
		recordShareLend(owner, agentID, it.id, people)
		who := ShareRecipientsText(recipients)
		if answer == credWrite {
			return "Credential " + it.Name + " → " + who + ": your key for everything, writes included, inside this agent only, until they connect their own. Their writes arrive as you."
		}
		return "Credential " + it.Name + " → " + who + ": your key for reads, inside this agent only; a write waits for them to connect their own account"
	case credWithhold:
		udb := UserDB(orchestrateBaseDB, owner)
		if a, ok := loadAgent(udb, agentID); ok && a.Owner == owner {
			if !namedIn(a.WithheldFromRecipients, it.id) {
				a.WithheldFromRecipients = append(a.WithheldFromRecipients, it.id)
				saveAgent(udb, a)
			}
		}
		return "Credential " + it.Name + ": withheld for the people you share this with; you keep it"
	case shareSkip:
		udb := UserDB(orchestrateBaseDB, owner)
		if a, ok := loadAgent(udb, agentID); ok && a.Owner == owner {
			if !namedIn(a.DisabledCredentials, it.id) {
				a.DisabledCredentials = append(a.DisabledCredentials, it.id)
				saveAgent(udb, a)
			}
		}
		return "Credential " + it.Name + ": turned off for this agent, for everybody"
	default:
		return "Credential " + it.Name + ": they supply their own of this name, so their calls go out as them"
	}
}

func mergeRecipients(have, add []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, u := range append(append([]string{}, have...), add...) {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// manifestForAgent is what THIS person still has to supply before the agent
// does what it says.
//
// Computed by walking the agent's reach as if they were its only recipient,
// which is the same question the panel answers for the owner asked from the
// other side. Per person, because the answer differs by person: one colleague
// already has a key of that name and another does not.
func manifestForAgent(owner, id, recipient string) []string {
	udb := UserDB(orchestrateBaseDB, owner)
	a, ok := loadAgent(udb, id)
	if !ok || a.Owner != owner {
		return nil
	}
	a.AllowedUsers = []string{recipient}
	a.Everyone = false

	var out []string
	for _, it := range agentReachOf(udb, owner, a).Items {
		// Credentials, and only credentials. Everything else the agent uses
		// travels with it, so there is nothing for the recipient to fetch,
		// adopt or ask for — and a manifest that listed those would be giving
		// somebody homework that is already done.
		if !it.Gap {
			continue
		}
		// Asked of THEM, not of the owner. The reach walk answers "is this key
		// lent", which is the owner's question; the recipient's is "do I have
		// one of this name", and somebody who already does should be told
		// nothing rather than sent to add a second.
		if _, ok := Secure().Resolve(it.id, recipient); ok {
			continue
		}
		out = append(out, "It calls an API through a credential named \""+it.Name+
			"\". Add one of your own by that name in Extensions, or its calls will fail.")
	}
	return out
}

// carriedByAgent is what somebody holding this agent is actually running.
//
// The owner has had this view since the reach panel existed. The person on the
// other end has had nothing: they open an agent, it reads documents and calls
// tools, and every one of those belongs to somebody else. That they cannot
// reach any of it outside this agent is what makes it safe to hand over — it
// is not a reason to leave them guessing about what happens inside it.
//
// Names rather than counts, because the agent is going to cite these documents
// and name these tools at them anyway. Listing them up front discloses nothing
// a turn would not, and answers "whose is this" before rather than after.
func carriedByAgent(owner, id, viewer string) []string {
	udb := UserDB(orchestrateBaseDB, owner)
	a, ok := loadAgent(udb, id)
	if !ok || a.Owner != owner {
		return nil
	}
	var out []string
	for _, it := range agentReachOf(udb, owner, a).Items {
		if it.kind == "credential" {
			continue // the manifest's half: what they supply, not what arrives
		}
		out = append(out, it.Kind+" — "+it.Name)
	}
	if len(out) == 0 {
		return nil
	}
	return append([]string{"All of this is " + owner + "'s, readable through this agent and nowhere else. You cannot attach any of it to an agent of your own."}, out...)
}

// agentCredentialDecision sets one credential's mode for one agent, from
// the row in the reach table that raised it.
//
// The guided flow asks the same four questions once, at the moment of sharing.
// This is the same answer changed later, which is when it is actually needed:
// a key gets rotated, somebody joins, or the first answer was the cautious one
// and the agent has been failing for a week because of it.
func (T *OrchestrateApp) agentCredentialDecision(c *jsonapi.Call, body struct {
	Name string `json:"name"`
	Lend string `json:"lend"`
}) (any, error) {
	user, udb, agentID := c.User, c.UDB, c.R.PathValue("id")
	a, ok := loadAgent(udb, agentID)
	if !ok || a.Owner != user {
		return nil, errNotFound
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("which credential?")
	}
	// Leaving skip re-enables the key for the agent before anything else, so
	// "they bring their own" after "leave it out" does what it says rather than
	// setting a lend on a credential the agent still cannot dispatch through.
	if body.Lend != shareSkip && namedIn(a.DisabledCredentials, name) {
		a.DisabledCredentials = withoutOne(a.DisabledCredentials, name)
		if _, err := saveAgent(udb, a); err != nil {
			return nil, err
		}
	}
	// Moving off a lend takes the lend back. The guided flow never had to do
	// this because it only ever ran forward, from no decision to one; a control
	// that can be moved back implies the move undoes something, and a pill that
	// reads "they bring their own" over a key still lent would be a lie told by
	// the UI.
	if body.Lend == credOwn || body.Lend == shareSkip {
		withdrawOneCredentialLend(user, agentID, name)
	}
	it := credentialReach(user, name, namedIn(a.DisabledCredentials, name))
	msg := applyCredentialAnswer(user, agentID, it, a.AllowedUsers, body.Lend)
	return map[string]string{"result": msg}, nil
}
