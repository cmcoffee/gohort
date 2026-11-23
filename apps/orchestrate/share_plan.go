package orchestrate

// What handing an agent over decides, and what it carries.
//
// Everything an agent uses travels with it, scoped to it. A credential is the
// exception: it is whose account a call goes out as, so the owner chooses per
// key, on the agent's "What it reaches" (agentCredentialDecision): Mine for
// reads, Theirs, Mine with writes too, or Disallow. A guided share flow used
// to ask the same once, at sharing; nothing reached it, and it is gone.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
// The row is where the choice is made and changed: at sharing, when a key gets
// rotated, when somebody joins, or when the first answer was the cautious one
// and the agent has been failing for a week because of it. The choice is
// recorded as made (CredentialsDecided), so the row stops asking.
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
	switch body.Lend {
	case credRead, credOwn, credWrite, credWithhold, shareSkip:
	default:
		return nil, jsonapi.BadRequest("choose mine for reads, theirs, mine with writes too, or disallow")
	}
	// Leaving skip re-enables the key for the agent before anything else, so
	// "they bring their own" after "leave it out" does what it says rather than
	// setting a lend on a credential the agent still cannot dispatch through.
	// Leaving Disallow gives it back to the people the agent is shared with.
	// And the choice is recorded as made, so the row stops asking for one.
	if body.Lend != shareSkip {
		a.DisabledCredentials = withoutOne(a.DisabledCredentials, name)
	}
	if body.Lend != credWithhold {
		a.WithheldFromRecipients = withoutOne(a.WithheldFromRecipients, name)
	}
	if !namedIn(a.CredentialsDecided, name) {
		a.CredentialsDecided = append(a.CredentialsDecided, name)
	}
	if _, err := saveAgent(udb, a); err != nil {
		return nil, err
	}
	// Moving off a lend takes the lend back. A flow that only ran forward,
	// from no decision to one, never had to do this; a control
	// that can be moved back implies the move undoes something, and a pill that
	// reads "they bring their own" over a key still lent would be a lie told by
	// the UI.
	if body.Lend == credOwn || body.Lend == shareSkip || body.Lend == credWithhold {
		withdrawOneCredentialLend(user, agentID, name)
	}
	it := credentialReach(user, name, namedIn(a.DisabledCredentials, name))
	msg := applyCredentialAnswer(user, agentID, it, a.AllowedUsers, body.Lend)
	return map[string]string{"result": msg}, nil
}
