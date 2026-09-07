package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

// --- handlers ---------------------------------------------------------------

// foldUncheckedIntoDenyList recomputes a seed agent's DisabledPersistentTools
// from the Tools modal's picked (checked) set. Any of the user's persistent
// temp tools NOT in the picked set is added to the deny list; picked ones are
// removed so re-checking re-enables them. Only persistent temp tools can land
// on the deny list — framework/registered tools are gated by AllowedTools
// instead, so they're left untouched here. Map iteration order is irrelevant:
// the result is a stored set, not an LLM-facing schema.
func foldUncheckedIntoDenyList(db Database, user string, picked, currentDisabled []string) []string {
	pickedSet := make(map[string]bool, len(picked))
	for _, n := range picked {
		pickedSet[n] = true
	}
	disabledSet := make(map[string]bool, len(currentDisabled))
	for _, n := range currentDisabled {
		disabledSet[n] = true
	}
	for _, p := range LoadPersistentTempTools(db, user) {
		// Scoped rows are not part of THIS fold's vocabulary: `picked` is the
		// allowed_tools checklist, and a scoped tool is never one of those
		// options, so "not picked" says nothing about it. Folding them in
		// anyway re-disabled such a tool on every save — "every time I enable
		// it, it gets disabled", with nothing on screen to explain why.
		//
		// Skipped, NOT deleted: the Tools modal now renders scoped tools as
		// their own checklist group and sends its decisions in
		// DisabledPersistentTools directly. Deleting here would erase the
		// owner's explicit off the moment they saved it.
		if len(p.ScopeAgents) > 0 {
			continue
		}
		if pickedSet[p.Tool.Name] {
			delete(disabledSet, p.Tool.Name) // re-enabled
		} else {
			disabledSet[p.Tool.Name] = true // explicitly disabled
		}
	}
	out := make([]string, 0, len(disabledSet))
	for n := range disabledSet {
		out = append(out, n)
	}
	return out
}

// keepScopedDenials filters a deny list down to the SCOPED tools in it.
//
// The "everything checked" save on a default-pool seed clears the deny list —
// correct for pool tools, since checked means "on" and the empty list restores
// auto-include. But a scoped tool has no checkbox in that list at all: its
// on/off is its own group in the Tools modal, sent in the same field. Clearing
// wholesale would re-enable a tool the owner just switched off, in the same
// save that switched it off. Keep what the client said about scoped rows; drop
// the rest, which is what "all checked" means.
func keepScopedDenials(db Database, user string, disabled []string) []string {
	if len(disabled) == 0 {
		return nil
	}
	scoped := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, user) {
		if len(p.ScopeAgents) > 0 {
			scoped[p.Tool.Name] = true
		}
	}
	out := make([]string, 0, len(disabled))
	for _, n := range disabled {
		if scoped[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dropPickedDenials removes from a deny list every name the Tools modal just
// CHECKED. It is the other half of foldUncheckedIntoDenyList: a user-crafted
// agent expresses "off" by leaving a name OUT of AllowedTools, so its unchecks
// have nothing to fold — but a checked box still has to be able to clear a deny
// entry some other surface wrote, or the checkbox is decorative.
//
// Scoped rows are left alone: their decisions arrive in this same field from
// their own checklist group, and they are never in the picked set.
func dropPickedDenials(db Database, user string, picked, disabled []string) []string {
	if len(disabled) == 0 {
		return nil
	}
	scoped := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, user) {
		if len(p.ScopeAgents) > 0 {
			scoped[p.Tool.Name] = true
		}
	}
	pickedSet := make(map[string]bool, len(picked))
	for _, n := range picked {
		pickedSet[canonicalToolName(n)] = true
	}
	out := make([]string, 0, len(disabled))
	for _, n := range disabled {
		if pickedSet[canonicalToolName(n)] && !scoped[n] {
			continue // the box the owner just ticked
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// curateToolsFromModal translates ONE Tools-modal save into stored curation.
//
// The modal sends two things: the CHECKED catalog names as AllowedTools, and
// the scoped group's decisions already made in DisabledPersistentTools. A tool
// is on for an agent only when the allow-list admits it AND the deny list is
// silent about it, so the one surface that shows both gates has to write both.
// It is also the only save allowed to — see the preservation branch at the call
// site.
func curateToolsFromModal(db Database, user string, req *AgentRecord) {
	// The no-tools sentinel (["__none__"]) means exactly that; the runtime
	// handles it via noTools and there is no checked set to translate.
	if isNoToolsSentinel(req.AllowedTools) {
		return
	}
	seed, isSeed := seedAgentByID(req.ID)
	switch {
	case isSeed && len(seed.AllowedTools) == 0:
		// Default-pool seed (seed-chat): the in-code seed ships an EMPTY
		// AllowedTools, meaning "every approved tool, including ones approved
		// in the future". Unchecks fold into the deny list, and AllowedTools is
		// forced back to nil so it never freezes into a snapshot that blocks
		// auto-add.
		if len(req.AllowedTools) == 0 {
			// All-checked: clear the deny list, except what the modal said
			// about SCOPED tools — they have no checkbox in the list this
			// "all" describes (see keepScopedDenials).
			req.DisabledPersistentTools = keepScopedDenials(db, user, req.DisabledPersistentTools)
		} else {
			req.DisabledPersistentTools = foldUncheckedIntoDenyList(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
		req.AllowedTools = nil
	case isSeed:
		// Curated seed (research, kb): the in-code seed ships a real
		// framework-tool allowlist that resolveWorkerTools intersects against.
		// Preserve it as the literal picked list; only the user's persistent
		// temp-tool unchecks fold into the deny list. Wiping AllowedTools here
		// would broaden the agent to the full default pool (loadAgent does not
		// restore the curated list).
		if len(req.AllowedTools) > 0 {
			req.DisabledPersistentTools = foldUncheckedIntoDenyList(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
	default:
		// A user-crafted agent gates the catalog through AllowedTools alone, so
		// nothing here folds unchecks INTO the deny list — leaving the name out
		// of the allow-list already said it. Entries land there all the same:
		// the per-agent Scope pill's OFF writes one whenever the agent has no
		// allow-list to trim (disableGlobalToolForAgent). Until this branch
		// existed, nothing could ever take one back — re-checking the box saved
		// an allow-list the stale deny entry then overrode, so the tool read
		// unchecked again on every reload, permanently. Seeds were given this in
		// v0.5.698/699; agents the user made themselves were the case left
		// standing.
		if len(req.AllowedTools) == 0 {
			// Every catalog box checked: nothing in the pool is off. What the
			// scoped group said still stands — it is not part of that "all".
			req.DisabledPersistentTools = keepScopedDenials(db, user, req.DisabledPersistentTools)
		} else {
			req.DisabledPersistentTools = dropPickedDenials(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
	}
}
