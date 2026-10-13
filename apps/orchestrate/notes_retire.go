package orchestrate

// Agent-wide working notes, retired.
//
// A scratchpad scoped to an agent outlived the work it described, and every
// defect the layer had came from that: stale notes steering every turn, a
// parked call outliving the tool it named, an audit rule to find both. The
// work that wants a scratchpad has one now, scoped to the work: a scheduled
// task's notes, and a link group's shared note (task_notes.go). The two places
// that turned agent-wide notes on were the first-run wizard, whose "About you"
// is durable information and now lands as facts, and Anvil, which keys notes
// per project and still sets EnableNotes itself at runtime.
//
// What an agent had written is not dropped: once, at startup, each agent with
// notes switched on keeps their text as a fact and has them switched off. The
// runtime machinery stays for a hosting app that scopes an agent per thing.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// retireAgentNotes moves every agent's working notes into its facts and turns
// the notes off. Idempotent: an agent with notes off is not touched, so after
// the first pass it is one record walk per user.
func (T *OrchestrateApp) retireAgentNotes() {
	if AuthDB == nil || AuthDB() == nil {
		return
	}
	for _, u := range AuthListUsers(AuthDB()) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		for _, a := range listAgents(udb, u.Username) {
			if !a.EnableNotes || a.Owner != u.Username {
				continue
			}
			retireOneAgentNotes(udb, a)
		}
	}
}

// retireOneAgentNotes keeps one agent's notes as a fact and switches them off.
func retireOneAgentNotes(udb Database, a AgentRecord) {
	ns := factsNamespace(a.ID)
	if text := strings.TrimSpace(ResolveOperatingNotes(udb, ns, a.SeedNotes).Text); text != "" {
		StoreMemoryFact(udb, ns, "Working notes kept from before notes were retired: "+text)
	}
	a.EnableNotes, a.SeedNotes = false, ""
	if _, err := saveAgent(udb, a); err != nil {
		Log("[orchestrate/notes] %s: could not switch notes off: %v", a.Name, err)
		return
	}
	Log("[orchestrate/notes] %s: working notes kept as a fact and switched off", a.Name)
}
