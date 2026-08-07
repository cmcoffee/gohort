// Which apps have granted this agent anything, and where to go and change it.
//
// Six answers to "may this agent use that" already exist — OwnedBy,
// AllowedUsers, AgentScope, channel send authority, API-key tool scope, and
// servitor's per-machine command grants. Each was built for its own case and
// each has a genuinely different shape, which is the argument for a seam and
// the argument against a single permission model in equal measure.
//
// So this generalizes TWO of the three layers and deliberately not the third.
//
//	identity    which app, which agent          — uniform, lives here
//	visibility  what does it hold, where to edit — uniform, lives here
//	meaning     what the grant PERMITS           — app's own, stays there
//
// Forcing the third into a shared level enum is how these end up worse than
// what they replaced: risk categories, chat ids, credential names and tool
// allowlists do not reduce to Read/Write/Admin, and the moment they are made
// to, every app smuggles its real structure through a metadata blob and the
// abstraction has bought nothing but indirection.
//
// WHAT IS ENFORCED HERE is the one rule that IS uniform: absent means denied.
// A grantor reports what an agent HOLDS; there is no path through this file by
// which an app can report a default. An app that wants a capability on for
// everyone has to say so per agent, where somebody can see it and take it away.
package core

import (
	"sort"
	"strings"
	"sync"
)

// AgentGrant is one thing an app has given one agent. Label is what a person
// recognizes it by; Detail says how much, in that app's own terms, because
// only the app knows what its permissions mean.
type AgentGrant struct {
	Label  string `json:"label"`            // "Lab Box", "#ops", "billing-api"
	Detail string `json:"detail,omitempty"` // "nothing without asking", "read-only"
}

// AgentGrantor is an app that can give agents access to things it owns.
//
// Granted returns what this agent HOLDS — never what it could hold, and never
// a default. An app with nothing granted returns nil, and the row reads "none",
// which is the honest answer and the one that makes an unexpected grant
// visible.
type AgentGrantor struct {
	// Name is the stable id, used for ordering and logging. Label is what the
	// agent editor shows, and should name the RESOURCE ("Machines"), not the
	// app — a person reading an agent's capabilities is asking what it can
	// reach, not which package implements it.
	Name  string
	Label string
	// Granted lists what this agent holds. Called on a page render, so it must
	// be cheap and must not block.
	Granted func(user, agentID string) []AgentGrant
	// ManageURL is where the owner goes to change it. The app owns the detail
	// UI, because the detail is the part that does not generalize.
	ManageURL string
}

var (
	agentGrantorMu sync.RWMutex
	agentGrantors  = map[string]AgentGrantor{}
)

// RegisterAgentGrantor installs one. Call at startup from the app that owns the
// records. A repeat registration replaces and says so, for the same reason the
// tool provider registry does: which one survived would otherwise depend on map
// iteration.
func RegisterAgentGrantor(g AgentGrantor) {
	if strings.TrimSpace(g.Name) == "" || g.Granted == nil {
		return
	}
	agentGrantorMu.Lock()
	defer agentGrantorMu.Unlock()
	if _, dup := agentGrantors[g.Name]; dup {
		Log("[grants] agent grantor %q registered twice — replacing the earlier one", g.Name)
	}
	agentGrantors[g.Name] = g
}

// AgentGrantSummary is one app's answer for one agent, ready to render.
type AgentGrantSummary struct {
	Name      string       `json:"name"`
	Label     string       `json:"label"`
	Grants    []AgentGrant `json:"grants,omitempty"`
	ManageURL string       `json:"manage_url,omitempty"`
	// Text is the row's own sentence — "2 machines" or "none" — so a caller
	// rendering a list does not have to reimplement the empty case and get it
	// subtly different from every other caller.
	Text string `json:"text"`
}

// AgentGrantSummaries reports what every registered app has given one agent,
// in a stable order.
//
// EVERY grantor appears, including those granting nothing. A row reading "none"
// is what tells an owner the capability exists and that this agent does not
// have it; omitting empties would make an app invisible until the moment it
// mattered, which is the wrong moment to discover it.
func AgentGrantSummaries(user, agentID string) []AgentGrantSummary {
	if strings.TrimSpace(agentID) == "" {
		return nil
	}
	agentGrantorMu.RLock()
	list := make([]AgentGrantor, 0, len(agentGrantors))
	for _, g := range agentGrantors {
		list = append(list, g)
	}
	agentGrantorMu.RUnlock()

	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]AgentGrantSummary, 0, len(list))
	for _, g := range list {
		grants := safeGranted(g, user, agentID)
		out = append(out, AgentGrantSummary{
			Name: g.Name, Label: labelOr(g.Label, g.Name),
			Grants: grants, ManageURL: g.ManageURL, Text: grantText(grants),
		})
	}
	return out
}

// safeGranted runs one grantor behind a recover. An app that panics while
// listing grants reports NOTHING — never everything — because the failure mode
// of a permissions display that guesses is that somebody reads a capability
// their agent does not have, or misses one it does.
func safeGranted(g AgentGrantor, user, agentID string) (out []AgentGrant) {
	defer func() {
		if r := recover(); r != nil {
			Log("[grants] grantor %q panicked listing grants for agent %q (%v) — reporting none", g.Name, agentID, r)
			out = nil
		}
	}()
	return g.Granted(user, agentID)
}

// grantText renders the row's sentence. The empty case is a word, not a dash:
// "none" is a statement about this agent, and a dash reads like missing data.
func grantText(grants []AgentGrant) string {
	switch len(grants) {
	case 0:
		return "none"
	case 1:
		return grants[0].Label
	case 2:
		return grants[0].Label + " and " + grants[1].Label
	}
	return grants[0].Label + ", " + grants[1].Label + " and " + intToString(len(grants)-2) + " more"
}

func labelOr(label, fallback string) string {
	if l := strings.TrimSpace(label); l != "" {
		return l
	}
	return fallback
}
