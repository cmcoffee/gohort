package orchestrate

// What an agent reaches, and whether the people it is shared with reach it too.
//
// Sharing an agent is one request — "my team and I should all be working from
// this" — and gohort answers it with seven. The tools it calls, the documents
// it reads, the skills it activates, the recipes it dispatches and the key
// underneath all of it are separate records on separate rungs, each shared by
// its own door. Nothing was wrong with that except that the person has to hold
// the whole graph in their head, share each piece to the same people in the
// right order, and find out afterwards from a notice which ones they missed.
//
// This walks the graph instead. It is deliberately READ-ONLY: it reports what
// an agent depends on, how far each dependency reaches today, and which of them
// a recipient would come up short on. Nothing here shares anything. Getting the
// walk right — and in particular getting the RESOLUTION rules right, since they
// differ per kind — has to come before anything acts on it, because a preview
// that quietly misdescribes one edge is worse than no preview at all.
//
// The resolution rules, which are the whole substance of this file:
//
//   - TOOLS resolve BY NAME from the runner's own catalog. A recipient needs a
//     tool of that name; yours reaches them only if it is shared or global.
//   - BUNDLED tools live on the agent record itself, so they travel with it.
//   - SKILLS resolve BY ID against the runner's available set, which is their
//     own plus what was shared with them plus what the deployment publishes. A
//     same-named skill of their own is a DIFFERENT id and will not stand in.
//   - COLLECTIONS resolve BY ID, gated per runtime user. Yours reaches them
//     only if it is shared with them or deployment-wide.
//   - PIPELINES and MACHINES resolve BY ID against their own plus what is
//     shared with them plus what is published.
//   - CREDENTIALS resolve BY NAME in the runner's namespace: their own of that
//     name wins, then one lent to them, then the deployment's.

import (
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// How far a dependency reaches. Ordered, so "does this reach as far as the
// agent" is a comparison rather than a table of special cases.
const (
	reachPrivate    = iota // only its owner
	reachNamed             // shared with specific people
	reachDeployment        // everybody
	// reachTravels is carried ON the agent record, so it reaches exactly as
	// far as the agent does — whatever that turns out to be. It sits at the
	// top of the ordering because it can never be the short one: an agent
	// shared with two people carries its bundled tools to both, and an agent
	// published to everybody carries them to everybody.
	reachTravels
)

// reachItem is one thing an agent depends on.
type reachItem struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Reach and How are the two halves a reader needs: how far this thing
	// goes today, and how a recipient's run would look for it. The second is
	// what makes a gap understandable rather than just flagged.
	Reach string `json:"reach"`
	How   string `json:"how"`
	// Gap is true when this dependency reaches less far than the agent does,
	// so somebody who has the agent would not have this.
	Gap     bool   `json:"gap"`
	Missing string `json:"missing,omitempty"`

	level int // reach, for comparison; not serialized
}

// agentReachMap is the whole picture for one agent.
type agentReachMap struct {
	Items []reachItem `json:"items"`
	// Audience is how far the AGENT goes, which is what every dependency is
	// measured against. An agent nobody has is not missing anything.
	Audience   string `json:"audience"`
	Gaps       int    `json:"gaps"`
	audience   int
	recipients []string
}

// agentReachOf walks one agent's dependencies in the owner's namespace.
func agentReachOf(udb Database, owner string, a AgentRecord) agentReachMap {
	out := agentReachMap{audience: reachPrivate, recipients: a.AllowedUsers}
	switch {
	case a.Exposed || a.MCPExposed:
		out.audience, out.Audience = reachDeployment, "Published to everybody"
	case len(a.AllowedUsers) > 0:
		out.audience = reachNamed
		out.Audience = "Shared with " + strings.Join(a.AllowedUsers, ", ")
	default:
		out.Audience = "Private to you"
	}

	creds := map[string]bool{}
	add := func(it reachItem) {
		it.Gap = it.level < out.audience
		if it.Gap {
			it.Missing = missingFor(out.audience, out.recipients)
			out.Gaps++
		}
		out.Items = append(out.Items, it)
	}

	// Bundled tools: part of the record, so they go wherever it goes.
	for _, t := range a.Tools {
		if c := strings.TrimSpace(t.Credential); c != "" {
			creds[c] = true
		}
		add(reachItem{Kind: "Tool", Name: t.Name, Reach: "Travels with the agent",
			How: "Defined on the agent itself, so anybody who has the agent has it.", level: reachTravels})
	}

	// Named tools: resolved by NAME from whoever is running.
	pool := map[string]PersistentTempTool{}
	for _, p := range LoadPersistentTempTools(AuthDB(), owner) {
		pool[p.Tool.Name] = p
	}
	for _, name := range a.AllowedTools {
		name = strings.TrimSpace(name)
		p, mine := pool[name]
		if name == "" || !mine {
			// Not in this owner's pool: a framework tool, or one from the
			// shared catalog, or a name nothing answers to. The first two
			// resolve for a recipient exactly as they do for the owner, and
			// the third is already the broken-dependency surface's subject.
			// None of them is something the owner could share even if they
			// wanted to, so a row for it would be a line nobody can act on.
			continue
		}
		if c := strings.TrimSpace(p.Tool.Credential); c != "" {
			creds[c] = true
		}
		level, reach := reachPrivate, "Private to you"
		switch {
		case p.Shared && len(p.AllowedUsers) == 0:
			level, reach = reachDeployment, "In the shared catalog"
		case p.Shared:
			level, reach = reachNamed, "Shared with "+strings.Join(p.AllowedUsers, ", ")
		}
		add(reachItem{Kind: "Tool", Name: name, Reach: reach,
			How:   "Resolved by name from the runner's own catalog, so they need a tool called this.",
			level: level})
	}

	// Skills: by ID, against what the runner can actually use.
	skills := map[string]SkillRecord{}
	for _, s := range LoadSkills(udb, owner) {
		skills[s.ID] = s
	}
	published := map[string]bool{}
	for _, s := range DeploymentSkills(udb) {
		published[s.ID] = true
		skills[s.ID] = s
	}
	for _, id := range a.AllowedSkills {
		s, ok := skills[strings.TrimSpace(id)]
		if !ok {
			add(reachItem{Kind: "Skill", Name: id, Reach: "Not found",
				How: "Attached by id, and no skill of yours answers to it. It does nothing for you either.", level: reachDeployment})
			continue
		}
		level, reach := reachPrivate, "Private to you"
		switch {
		case published[s.ID]:
			level, reach = reachDeployment, "Deployment-wide"
		case len(s.AllowedUsers) > 0:
			level, reach = reachNamed, "Shared with "+strings.Join(s.AllowedUsers, ", ")
		}
		add(reachItem{Kind: "Skill", Name: s.Name, Reach: reach,
			How:   "Attached by id. A skill of their own with the same name is a different id and will not stand in.",
			level: level})
	}

	// Collections: by id, gated per runtime user.
	for _, id := range a.AttachedCollections {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		c, ok := LoadCollection(UserDB(CollectionsDB(), owner), owner, id)
		if !ok {
			add(reachItem{Kind: "Knowledge", Name: id, Reach: "Not found",
				How: "Attached by id, and no collection of yours answers to it.", level: reachDeployment})
			continue
		}
		level, reach := reachPrivate, "Private to you"
		switch {
		case IsDeploymentScope(c):
			level, reach = reachDeployment, "Deployment-wide"
		case len(c.AllowedUsers) > 0:
			level, reach = reachNamed, "Shared with "+strings.Join(c.AllowedUsers, ", ")
		}
		add(reachItem{Kind: "Knowledge", Name: c.Name, Reach: reach,
			How:   "Resolved as whoever is running, so they see it only if it was shared with them.",
			level: level})
	}

	// Recipes: pipelines by id, then the one machine.
	for _, id := range a.AttachedPipelines {
		if def, ok := LoadPipelineDef(udb, owner, strings.TrimSpace(id)); ok {
			level, reach := reachPrivate, "Private to you"
			switch {
			case def.Published:
				level, reach = reachDeployment, "Deployment-wide"
			case len(def.AllowedUsers) > 0:
				level, reach = reachNamed, "Shared with "+strings.Join(def.AllowedUsers, ", ")
			}
			add(reachItem{Kind: "Pipeline", Name: def.Name, Reach: reach,
				How: "Dispatched by id. It runs in the namespace of whoever started it.", level: level})
		}
	}
	if id := strings.TrimSpace(a.Machine); id != "" {
		if def, ok := LoadMachineDef(udb, owner, id); ok {
			level, reach := reachPrivate, "Private to you"
			switch {
			case def.Published:
				level, reach = reachDeployment, "Deployment-wide"
			case len(def.AllowedUsers) > 0:
				level, reach = reachNamed, "Shared with "+strings.Join(def.AllowedUsers, ", ")
			}
			add(reachItem{Kind: "Machine", Name: def.Name, Reach: reach,
				How: "Run by id. It runs in the namespace of whoever started it.", level: level})
		}
	}

	// Credentials last, because they are what the tools above rest on and the
	// one kind whose answer is usually "each person supplies their own".
	var credNames []string
	for name := range creds {
		credNames = append(credNames, name)
	}
	sort.Strings(credNames)
	for _, name := range credNames {
		add(credentialReach(owner, name, namedIn(a.DisabledCredentials, name)))
	}
	return out
}

// credentialReach is its own function because a credential answers the
// question differently from everything else above it. The others are "do they
// have a copy"; this one is "whose identity does the call go out as", and the
// ordinary answer for a team is that each person supplies their own key rather
// than borrowing one.
func credentialReach(owner, name string, disabled bool) reachItem {
	it := reachItem{Kind: "Credential", Name: name}
	if disabled {
		it.Reach, it.How, it.level = "Turned off for this agent", "This agent cannot dispatch through it at all.", reachDeployment
		return it
	}
	c, mine := Secure().LoadUser(owner, name)
	if !mine {
		if _, global := Secure().Load(name); global {
			it.Reach, it.How, it.level = "The deployment's", "Everybody resolves this name to the deployment's credential.", reachDeployment
			return it
		}
		it.Reach, it.How, it.level = "Not yours", "Nothing of yours answers to this name, so the runner resolves it exactly as you do.", reachDeployment
		return it
	}
	lent := len(c.SharedReadOnly) + len(c.SharedReadWrite)
	switch {
	case lent == 0:
		it.Reach, it.level = "Personal, not lent", reachPrivate
		it.How = "Resolved by name as whoever is running. They will use a key of their own called this, or the call fails — which is usually what you want: their calls should go out as them."
	default:
		it.Reach, it.level = "Lent to "+strconv.Itoa(lent)+" person(s)", reachNamed
		it.How = "Resolved by name as whoever is running: their own first, then yours if you lent it to them. Anything they write through yours arrives as YOU."
	}
	return it
}

// missingFor says who comes up short, in their own terms rather than as a
// severity. "Nobody you shared this with has it" is something to act on;
// "warning" is not.
func missingFor(audience int, recipients []string) string {
	if audience == reachDeployment {
		return "Not everybody has this"
	}
	switch len(recipients) {
	case 0:
		return "Not shared as widely as the agent"
	case 1:
		return recipients[0] + " does not have this"
	default:
		return strings.Join(recipients, " and ") + " do not have this"
	}
}

func namedIn(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
