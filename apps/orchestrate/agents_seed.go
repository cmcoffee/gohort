package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// isSeedID reports whether the given ID belongs to a framework-defined
// seed. Used at storage boundaries to switch between "user record"
// and "shadow / revert-to-default" semantics.
func isSeedID(id string) bool {
	_, ok := seedAgentByID(id)
	return ok
}

// fleetHidden reports an agent that must not appear on ANY discovery surface —
// not a picker, not a dispatch list, and not the Builder's survey.
//
// Four separate reasons converge on the same answer, and they had been spelled
// out inline at each call site with different subsets: some checked all four,
// the dispatch-discovery paths checked only the two seed predicates. That is
// how a Hidden app agent (the Servitor Investigator, a per-appliance TEMPLATE)
// and the retired Chat / Research / Knowledge Base seeds all turned up in a
// survey of the fleet, presented to the Builder as things it could reuse or
// dispatch to.
//
// One predicate so the answer cannot differ by surface. Contextual exclusions
// (self, Builder) stay at their call sites — those depend on who is asking.
func fleetHidden(id string) bool {
	return hiddenAppAgent(id) || isCloneOnlySeed(id) ||
		isFleetRetiredSeed(id) || isRetiringArchetypeSeed(id)
}

// isFleetRetiredSeed reports a framework seed that is structurally OUT of the
// agent-to-agent dispatch surface as well as the user pickers — nothing lists
// it, gets it, or runs it, and an unhidden shadow or an explicit dispatch
// allowlist pick must not resurrect it. seed-chat is the only member: fully
// retired, its record kept solely for legacy sessions and shadows.
// seed-research / seed-kb are the SOFT-retired archetype seeds
// (isRetiringArchetypeSeed) — they materialize a user-owned copy on dispatch
// rather than refuse. Builder has its own exclusion (isBuilderAgent) with
// different, human-in-the-loop reasoning.
func isFleetRetiredSeed(id string) bool { return id == "seed-chat" }

// isRetiringArchetypeSeed reports the framework PERSONA seeds being retired in
// favor of Builder archetypes (see archetypes.go): Research and Knowledge
// Base. Unlike a hard-retired seed (isFleetRetiredSeed, which refuses), these
// SOFT-retire: dropped from the dispatch-discovery surfaces (no agent sees
// them as a peer, no picker offers them), but a live dispatch to one
// materializes a USER-OWNED copy and runs that — so a standing mission that
// dispatches to "Research" keeps working, now against the user's own agent.
// The virgin seed still resolves by id (loadAgent → seedAgentByID) so the
// wizard template + the materialize clone can read its config.
func isRetiringArchetypeSeed(id string) bool {
	return id == "seed-research" || id == "seed-kb"
}

// materializeArchetypeAgent turns a retiring archetype seed into a real
// user-owned agent for owner: an ordinary editable/deletable agent carrying
// the seed's vetted config, named the same so name-resolution keeps finding
// it. Idempotent — a second call (or a by-id dispatch after the first) returns
// the existing copy instead of duplicating. Only the VIRGIN seed is
// materialized; a user who already SHADOWED the seed (customized it, so their
// row is Owner=user at the seed id) keeps that shadow untouched — the caller
// checks target.Owner == seedOwner before calling here.
func materializeArchetypeAgent(db Database, owner, seedID string) (AgentRecord, bool) {
	seed, ok := seedAgentByID(seedID)
	if !ok {
		return AgentRecord{}, false
	}
	// Idempotency: an existing user-owned, non-seed agent with the seed's name
	// IS the materialized copy (covers a repeat by-id dispatch — by-name
	// resolves to it directly).
	for _, a := range listAgents(db, owner) {
		if a.Owner == owner && !isSeedID(a.ID) && strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(seed.Name)) {
			return a, true
		}
	}
	clone, err := cloneAgent(db, seedID, owner, seed.Name, false)
	if err != nil {
		Log("[orchestrate.archetype] materialize %q for %s failed: %v", seedID, owner, err)
		return AgentRecord{}, false
	}
	Log("[orchestrate.archetype] materialized user-owned %q (%s) for %s from %s", seed.Name, clone.ID, owner, seedID)
	return clone, true
}

// materializeIfRetiringSeed swaps a resolved dispatch target that is a VIRGIN
// retiring archetype seed for a freshly-materialized user-owned copy. A shadow
// (Owner=user at the seed id) or an already-user-owned agent passes through
// unchanged. The single seam every dispatch resolver calls right after
// findAgentByNameOrID so retirement never breaks a live dispatch.
func materializeIfRetiringSeed(db Database, owner string, target AgentRecord) AgentRecord {
	if target.Owner == seedOwner && isRetiringArchetypeSeed(target.ID) {
		if mat, ok := materializeArchetypeAgent(db, owner, target.ID); ok {
			return mat
		}
	}
	return target
}

// seedAgentByID returns the in-code seed with the given ID. Cheap —
// seedAgents() is a small slice walked at startup-frequency callsites
// (loadAgent miss path, isSeedID).
func seedAgentByID(id string) (AgentRecord, bool) {
	if id == "" {
		return AgentRecord{}, false
	}
	for _, a := range seedAgents() {
		if a.ID == id {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// isShadowed reports whether the user has saved a customization on
// top of the given seed. Used by the editor + agent_crud_tools to
// decide whether to expose "Revert" or "(starter, edit me)".
func isShadowed(db Database, id string) bool {
	if db == nil || !isSeedID(id) {
		return false
	}
	var a AgentRecord
	return db.Get(agentsTable, id, &a)
}

// cloneAgent creates a fresh agent owned by the caller, copying the
// persona fields from the source. The new agent gets a fresh ID and
// no session history — that's the whole point of cloning. Used when
// the user wants two named workspaces sharing one persona, or wants
// to customize a seed without mutating the original.
//
// promote=true clears OwnedBy on the clone, turning a sub-agent into
// a first-class top-level agent. This is the only path for surfacing
// a sub-agent's persona as a standalone surface — the editor can't
// flip the field (sub-agent posture is structurally pinned), so the
// clone-with-promotion flow is the dedicated escape hatch when the
// user wants to take a Builder-authored specialist and run it
// independently of its parent.
func cloneAgent(db Database, srcID, owner, newName string, promote bool) (AgentRecord, error) {
	src, ok := loadAgent(db, srcID)
	if !ok {
		return AgentRecord{}, fmt.Errorf("agent %q not found", srcID)
	}
	// Anyone can clone an agent visible to them (their own + seeds).
	if src.Owner != owner && src.Owner != seedOwner {
		return AgentRecord{}, fmt.Errorf("agent %q is not yours", srcID)
	}
	if strings.TrimSpace(newName) == "" {
		newName = src.Name + " (copy)"
	}
	clone := src
	clone.ID = ""
	clone.Owner = owner
	clone.Name = strings.TrimSpace(newName)
	clone.Created = time.Time{}
	clone.Tools = nil // flattened namespace: kit membership is store scope, not record copies
	// A copy of a SHAPE tracks that shape: everything the owner does not go on
	// to decide keeps coming from the framework, so a fix to the research
	// prompt reaches the research agent somebody cloned months ago. Copying a
	// user's own agent tracks nothing, because there is nothing behind it to
	// track. The overlay bookkeeping is reset either way and recomputed on
	// save; inheriting the source's list would claim the source's decisions as
	// this record's own.
	clone.ShapeID = ""
	clone.OverriddenFields = nil
	clone.OverlayRev = 0
	if shape, ok := shapeForSeed(src.ID); ok {
		clone.ShapeID = shape
	}
	if promote {
		clone.OwnedBy = ""
	}
	saved, err := saveAgent(db, clone)
	if err != nil {
		return saved, err
	}
	// The source's agent-scoped tools are SHARED with the clone by extending
	// each record's ScopeAgents — one name is one tool, so a clone cannot get
	// its own diverging copy. (To specialize a clone's tool, author a new
	// name for it via Builder.)
	for _, p := range AgentScopedTools(db, owner, src.ID) {
		SetUserToolScopeAgents(db, owner, p.Tool.Name,
			append(append([]string{}, p.ScopeAgents...), saved.ID))
	}
	return saved, nil
}

// seedOwner is the Owner string the in-code seeds carry. Returned
// to callers from loadAgent / listAgents so the editor can detect
// "this is a virgin seed, no shadow saved yet" and treat the record
// as read-only-until-edited.
const seedOwner = "system"

// sandboxPythonNoteSection returns the runtime-probed Python
// compatibility block, prefixed with "\n\n" so it concatenates cleanly
// at the end of a seed prompt or worker directives constant. Empty
// when Python is 3.7+ or the probe failed — the appended literal is
// just an empty string in that case, so the prompt is unchanged.
//
// Wrapped so callers don't have to remember the leading newlines. Reached
// from two places: the worker directives constant appends it directly, and
// the Builder seed document splices it in with a {{sandbox_python_note}}
// placeholder (see seeds_file.go). The separator lives in here rather than in
// the seed file so an empty note appends nothing at all instead of leaving a
// gap at the end of the prompt.
func sandboxPythonNoteSection() string {
	note := SandboxPythonAuthoringNote()
	if note == "" {
		return ""
	}
	return "\n\n" + note
}

// seedAgents returns the built-in starters. Stable IDs so they stay
// recognizable across rebuilds. Users clone these to customize.
// coreSeedAgents are the framework's own agents, all of them documents: the
// built-ins under builtin/ (Builder alone) plus the agent each SHAPE ships
// under archetypes/. seedAgents() (see app_agents.go) wraps this to also fold
// in cross-app registered App Agents, so all three resolve through the same
// overlay machinery.
func coreSeedAgents() []AgentRecord {
	return append(builtinAgents(), archetypeRecords()...)
}
