package orchestrate

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// A seed shadow is an OVERLAY, not a copy.
//
// A user's record at a seed's ID used to win entirely, so the moment anything
// wrote one (approving a tool, saving a rule, a scope decision) the user's copy
// of every OTHER field froze at that instant, and no framework improvement ever
// reached them again. The symptom was a flat input-token count across
// redeploys even after prompt edits.
//
// The fix so far was a hand-written list inside loadAgent: refresh the prompt,
// then the description, then Mode, then Cortex and Fleet, then PreMortem, then
// Hidden and ForcePrivate for app agents. Seven rules, each added after a
// separate bug report, each a field somebody noticed had frozen. Every field
// nobody has noticed yet is still frozen.
//
// So record what the user actually CHANGED, and inherit everything else. An
// agent that never touched its worker-round budget tracks the seed's budget
// forever; one that set it keeps its own, permanently, with no merge prompt,
// because an override is not a conflict, it is an answer.
//
// The stored record stays a full snapshot so that every caller reading the
// table directly keeps seeing a plausible record. OverriddenFields is what
// makes it authoritative: loadAgent starts from the seed and takes only those
// fields from the stored row.

// frameworkOwnedSeedFields are the json names a seed shadow may never
// override. They are the same set loadAgent used to refresh by hand, and the
// reasoning is unchanged: they define what the agent IS rather than how this
// deployment tuned it. A user who wants different answers to these clones.
func frameworkOwnedSeedFields(id string) map[string]bool {
	owned := map[string]bool{
		// The persona itself, and the line describing it.
		"orchestrator_prompt": true,
		"description":         true,
		// The agent's TYPE. A minimal shadow written by a tool approval has
		// these empty and would otherwise silently demote a cortex agent to a
		// plain chat one, taking the pinned thread and the controller nav
		// with it.
		"mode":    true,
		"channel": true,
		"fleet":   true,
		// A code-owned behavior default with no user toggle.
		"pre_mortem": true,
	}
	if _, isApp := appagents.AppAgentByID(id); isApp {
		// An app agent's visibility belongs to the app that registered it,
		// not to the user. A stale shadow minted when a tool got mis-scoped
		// onto one otherwise pins Hidden at whatever it was then, so flipping
		// the spec to Hidden:true never takes and the app agent keeps showing
		// in pickers and scope pills. Regular seeds keep their shadow's
		// Hidden: a user may legitimately hide their own agent.
		owned["hidden"] = true
		// ForcePrivate is framework-owned in ONE direction; see
		// ratchetAppAgentPrivacy.
		owned["force_private"] = true
	}
	return owned
}

// agentOverrides returns the json names on which rec differs from base,
// skipping the fields the framework owns. This is both how a save records what
// the user chose and how a legacy shadow is read: a shadow written before
// overlays carries no list, and every field where it differs from the seed is
// the best available evidence of an intentional change.
func agentOverrides(base, rec AgentRecord, owned map[string]bool) []string {
	bv, rv := reflect.ValueOf(base), reflect.ValueOf(rec)
	var out []string
	for name, idx := range agentFieldsByJSONName() {
		if owned[name] || identityField[name] {
			continue
		}
		if !reflect.DeepEqual(bv.Field(idx).Interface(), rv.Field(idx).Interface()) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// applyAgentOverrides layers a shadow's chosen fields onto the seed.
//
// A name this build does not know is skipped rather than refused: a record
// written by a newer build and read by an older one should lose one field, not
// fail to load an agent.
func applyAgentOverrides(base, over AgentRecord, fields []string, owned map[string]bool) AgentRecord {
	out := base
	ov := reflect.ValueOf(&out).Elem()
	sv := reflect.ValueOf(over)
	byName := agentFieldsByJSONName()
	for _, name := range fields {
		if owned[name] || identityField[name] {
			continue
		}
		idx, ok := byName[name]
		if !ok {
			continue
		}
		ov.Field(idx).Set(sv.Field(idx))
	}
	return out
}

// resolveSeedShadow is the whole overlay: the framework's record, wearing the
// user's decisions.
func resolveSeedShadow(seed, shadow AgentRecord) AgentRecord {
	fields := shadow.OverriddenFields
	if shadow.OverlayRev == 0 {
		// A shadow from before overlays. Its differences ARE its overrides;
		// nothing else about it can be trusted to be a choice. Resolving this
		// way leaves such a user seeing exactly what they saw yesterday, and
		// the list becomes explicit the next time they save.
		fields = agentOverrides(seed, shadow, frameworkOwnedSeedFields(seed.ID))
	}
	out := applyAgentOverrides(seed, shadow, fields, frameworkOwnedSeedFields(seed.ID))
	// Identity always comes from the stored row: it is what makes this the
	// user's record rather than the framework's.
	out.ID = shadow.ID
	out.Owner = shadow.Owner
	out.Created = shadow.Created
	out.Updated = shadow.Updated
	out.OverriddenFields = fields
	out.OverlayRev = shadow.OverlayRev
	return ratchetAppAgentPrivacy(seed, out)
}

// ratchetAppAgentPrivacy keeps ForcePrivate framework-owned in one direction
// only. A spec that declares itself private must reach a stale shadow, and a
// spec that does not must never clear a flag the user turned on for their own
// copy. Privacy ratchets up.
func ratchetAppAgentPrivacy(seed, out AgentRecord) AgentRecord {
	if _, isApp := appagents.AppAgentByID(out.ID); isApp {
		out.ForcePrivate = out.ForcePrivate || seed.ForcePrivate
	}
	return out
}

// identityField names the fields that say WHICH record this is rather than what
// it does. They are never overrides and never inherited.
var identityField = map[string]bool{
	"id":                true,
	"owner":             true,
	"created":           true,
	"updated":           true,
	"overridden_fields": true,
	"shape_id":          true,
	"overlay_rev":       true,
}

// agentFieldsByJSONName maps an AgentRecord's json name to its field index.
// Built once; the shape of the struct does not change at runtime.
func agentFieldsByJSONName() map[string]int {
	agentFieldsOnce.Do(func() {
		t := reflect.TypeOf(AgentRecord{})
		agentFields = make(map[string]int, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported
				continue
			}
			name := f.Tag.Get("json")
			if comma := strings.Index(name, ","); comma >= 0 {
				name = name[:comma]
			}
			if name == "" || name == "-" {
				continue
			}
			agentFields[name] = i
		}
	})
	return agentFields
}

var (
	agentFieldsOnce sync.Once
	agentFields     map[string]int
)

// --- instances -------------------------------------------------------------
//
// A tracking instance is the same overlay pointed at a shape instead of at a
// seed, with one difference: nothing is framework-owned. A seed shadow may not
// rewrite the persona, because it IS the framework's agent and cloning is the
// path to a different one. An instance already is the user's own agent, so
// every field is theirs to take, and the shape is only where the answers come
// from until they give one.

// instanceOwnedFields are recorded as the instance's own whether or not they
// differ from the shape. A name is how its owner refers to the agent, and
// renaming an agent somebody talks to daily because the framework renamed a
// shape is jarring with no upside, unlike a prompt fix.
var instanceOwnedFields = map[string]bool{"name": true}

// shapeBaseRecord returns the record a shape instantiates: the seed the
// archetype names. A shape with no seed describes an agent whose subject
// varies (a watcher, an investigator), so there is nothing to instantiate and
// nothing to track.
func shapeBaseRecord(shapeID string) (AgentRecord, bool) {
	if shapeID == "" {
		return AgentRecord{}, false
	}
	doc, ok := archetypeBySlug(shapeID)
	if !ok || doc.Seed == "" {
		return AgentRecord{}, false
	}
	return seedAgentByID(doc.Seed)
}

// instanceOverrides is what a tracking instance has decided for itself.
func instanceOverrides(base, rec AgentRecord) []string {
	fields := agentOverrides(base, rec, nil)
	for name := range instanceOwnedFields {
		if !hasStringField(fields, name) {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}

// resolveShapeInstance layers an instance's decisions onto its shape. An
// instance whose shape is gone, or which was written before it tracked
// anything, is returned as the standalone record it already is: the stored row
// is a full agent, so losing the link costs nothing but future updates.
func resolveShapeInstance(rec AgentRecord) AgentRecord {
	if rec.ShapeID == "" || rec.OverlayRev == 0 {
		return rec
	}
	base, ok := shapeBaseRecord(rec.ShapeID)
	if !ok {
		return rec
	}
	out := applyAgentOverrides(base, rec, rec.OverriddenFields, nil)
	out.ID = rec.ID
	out.Owner = rec.Owner
	out.Created = rec.Created
	out.Updated = rec.Updated
	out.ShapeID = rec.ShapeID
	out.OverriddenFields = rec.OverriddenFields
	out.OverlayRev = rec.OverlayRev
	// An instance is never the framework's record, whatever the shape says.
	out.OwnedBy = rec.OwnedBy
	out.Locked = rec.Locked
	return out
}

// shapeForSeed returns the archetype slug that ships a given seed, which is
// what a clone of that seed should track.
func shapeForSeed(seedID string) (string, bool) {
	if seedID == "" {
		return "", false
	}
	for _, doc := range loadArchetypes() {
		if doc.Seed == seedID {
			return doc.Slug, true
		}
	}
	return "", false
}

func hasStringField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}

// detachAgentFromShape stops an instance tracking, keeping the agent exactly as
// it reads today.
//
// The resolved record is what gets written, not the stored row. The row's
// unclaimed fields hold whatever the shape said when the agent was created, so
// writing it back would silently revert the agent to an older version of
// itself at the moment its owner asked to freeze it.
//
// One way on purpose. Re-attaching would have to decide which of the owner's
// fields were "really" theirs, and that is a question only they can answer, by
// building a new agent from the shape.
func detachAgentFromShape(db Database, id string) (AgentRecord, error) {
	if db == nil || id == "" {
		return AgentRecord{}, fmt.Errorf("agent %q not found", id)
	}
	rec, ok := loadAgent(db, id)
	if !ok {
		return AgentRecord{}, fmt.Errorf("agent %q not found", id)
	}
	if rec.ShapeID == "" {
		return rec, fmt.Errorf("%q does not follow a framework shape", rec.Name)
	}
	rec.ShapeID = ""
	rec.OverriddenFields = nil
	rec.OverlayRev = 0
	return saveAgent(db, rec)
}
