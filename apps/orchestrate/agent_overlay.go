package orchestrate

import (
	"reflect"
	"sort"
	"strings"
	"sync"

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
func agentOverrides(base, rec AgentRecord) []string {
	owned := frameworkOwnedSeedFields(rec.ID)
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
func applyAgentOverrides(seed, shadow AgentRecord, fields []string) AgentRecord {
	owned := frameworkOwnedSeedFields(seed.ID)
	out := seed
	ov := reflect.ValueOf(&out).Elem()
	sv := reflect.ValueOf(shadow)
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
		fields = agentOverrides(seed, shadow)
	}
	out := applyAgentOverrides(seed, shadow, fields)
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
