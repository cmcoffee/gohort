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
	"github.com/cmcoffee/gohort/core/shareledger"
)

// The answers a decision can carry. Values rather than booleans because a
// credential has four, and a two-state control that grew a third option is how
// the third one ends up meaning "whatever the second one meant".
const (
	shareSend = "send" // hand this dependency over too
	shareSkip = "skip" // leave it out; the agent works without it, or does not
	credOwn   = "own"  // they supply their own key of this name
	credRead  = "read" // lend mine, reads only
	credWrite = "write"
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
	a.Exposed, a.MCPExposed = false, false

	var out []shareledger.Decision
	for _, it := range agentReachOf(udb, owner, a).Items {
		if !it.Gap {
			continue
		}
		if it.kind == "credential" {
			out = append(out, credentialDecision(it, owner))
			continue
		}
		if d, ok := dependencyDecision(it); ok {
			out = append(out, d)
		}
	}
	// Credentials last: they are what the tools above rest on, and deciding
	// about a key before deciding whether the tool that spends it goes at all
	// is the wrong order to be asked in.
	sort.SliceStable(out, func(i, j int) bool {
		return credDecisionKey(out[i].Key) != credDecisionKey(out[j].Key) && !credDecisionKey(out[i].Key)
	})
	return out
}

func credDecisionKey(k string) bool { return strings.HasPrefix(k, "cred:") }

// dependencyDecision is the ordinary case: a skill, a collection, a tool, a
// recipe. Send it or leave it out.
func dependencyDecision(it reachItem) (shareledger.Decision, bool) {
	switch it.kind {
	case "skill", "collection", "pipeline", "machine", "tool":
	default:
		return shareledger.Decision{}, false
	}
	return shareledger.Decision{
		Key:   it.kind + ":" + it.id,
		Title: it.Kind + " — " + it.Name,
		Intro: it.Missing + ". " + it.How,
		Options: []shareledger.Choice{
			{Value: shareSend, Label: "Share it with them too",
				Help: "They get it on the same terms, through its own door."},
			{Value: shareSkip, Label: "Leave it out",
				Help: "The agent still runs; anything that needed this says it could not reach it."},
		},
		Default: shareSend,
	}, true
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
	lend, write := true, true
	if c, ok := Secure().LoadUser(owner, it.id); ok {
		lend, write = c.MayLend()
	}
	intro := "Whose identity should these calls go out as? This is the one dependency that is not a copy somebody is missing."
	opts := []shareledger.Choice{
		{Value: credOwn, Label: "They bring their own",
			Help: "The agent looks for a credential of this name in THEIR namespace. Their calls go out as them, which is usually what you want. If they have none, the tools that need it fail and say so."},
	}
	switch {
	case !lend:
		intro += " You have set this key to be lent to nobody, so the only answers left are theirs or none. Change that on the credential itself if you meant to lend it."
	case !write:
		opts = append(opts, shareledger.Choice{Value: credRead, Label: "Lend them mine, reads only",
			Help: "GET and HEAD through your key. Anything else is refused and the refusal is recorded against their name."})
		intro += " You have set this key to be lent for reads only, so a write lend is not offered."
	default:
		opts = append(opts,
			shareledger.Choice{Value: credRead, Label: "Lend them mine, reads only",
				Help: "GET and HEAD through your key. Anything else is refused and the refusal is recorded against their name."},
			shareledger.Choice{Value: credWrite, Label: "Lend them mine, reads and writes",
				Help: "What they write arrives at the far end as YOU: the page says you edited it. The ledger is the only place the two can be told apart."})
	}
	opts = append(opts, shareledger.Choice{Value: shareSkip, Label: "Leave it out",
		Help: "Turn this credential off for the agent entirely, for everybody including you."})
	return shareledger.Decision{
		Key:     "cred:" + it.id,
		Title:   "Credential — " + it.Name,
		Intro:   intro,
		Options: opts,
		Default: credOwn,
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
	out := []string{"Agent " + a.Name + " → " + strings.Join(recipients, ", ")}

	for _, it := range agentReachOf(udb, owner, a).Items {
		if !it.Gap {
			continue
		}
		answer := answers[it.kind+":"+it.id]
		if it.kind == "credential" {
			answer = answers["cred:"+it.id]
			out = append(out, applyCredentialAnswer(owner, id, it, recipients, answer))
			continue
		}
		if answer == shareSkip || answer == "" {
			out = append(out, it.Kind+" "+it.Name+": left out, as you asked")
			continue
		}
		added, err := grantToRecipients(udb, owner, it, recipients)
		switch {
		case err != nil:
			out = append(out, it.Kind+" "+it.Name+": "+err.Error())
		case len(added) > 0:
			recordFanout(owner, id, it, added)
			out = append(out, it.Kind+" "+it.Name+" → "+strings.Join(added, ", "))
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
		c, ok := Secure().LoadUser(owner, it.id)
		if !ok {
			return "Credential " + it.Name + ": not yours to lend, so they will need their own"
		}
		read, write := c.SharedReadOnly, c.SharedReadWrite
		if answer == credWrite {
			write = append(write, recipients...)
		} else {
			read = append(read, recipients...)
		}
		if err := Secure().SetCredentialShares(owner, it.id, read, write); err != nil {
			return "Credential " + it.Name + ": " + err.Error()
		}
		if answer == credWrite {
			return "Credential " + it.Name + " → " + strings.Join(recipients, ", ") + ", reads and writes. Their writes arrive as you."
		}
		return "Credential " + it.Name + " → " + strings.Join(recipients, ", ") + ", reads only"
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
	a.Exposed, a.MCPExposed = false, false

	var out []string
	for _, it := range agentReachOf(udb, owner, a).Items {
		if !it.Gap {
			// It reaches them — which is not the same as it being ready. A
			// shared tool sits in their catalog until they take it, and only
			// tools know that about themselves, so ask the kind rather than
			// growing a second opinion here.
			out = append(out, shareledger.Manifest(it.kind, owner, it.id, recipient)...)
			continue
		}
		switch it.kind {
		case "credential":
			// The one they can actually act on, and the one that is not a
			// copy of anything: it needs a key of theirs by that exact name.
			out = append(out, "It calls an API through a credential named \""+it.Name+
				"\". Add one of your own by that name in Extensions, or its calls will fail.")
		case "tool":
			out = append(out, "It uses the tool \""+it.Name+"\", which has not reached you. Ask "+owner+" to share it.")
		default:
			out = append(out, "It uses the "+strings.ToLower(it.Kind)+" \""+it.Name+"\", which has not reached you.")
		}
	}
	return out
}
