// The settings an agent can answer for itself, and what answers when it has not.
//
// A setting stored as a bool on an agent cannot express this. "Off because the
// default is off" and "off because I set it" are the same false, and the
// difference only appears later, when the default changes: one agent should
// follow and the other should not, and nothing recorded which was which.
//
// So a setting that can be defaulted is stored as a STRING, empty meaning "not
// decided here". The same reason gob cannot carry a *bool in this codebase: a
// pointer that must distinguish unset from false does not survive the round
// trip, and a string does.
//
// THERE IS ONE DEFAULT AND AN ADMINISTRATOR SETS IT. There used to be a
// per-owner rung here as well - every owner carried their own default for
// their own fleet - and it was one rung too many. Two pages could answer the
// same question, an owner reading theirs could not see the deployment's, and
// the layer bought nothing a per-agent answer did not already buy. See
// deployment_defaults.go, which now holds both the default and the ceiling.

package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// The settings that can carry a fleet default. Named, not open: a key nobody
// reads is a control that appears to work.
const (
	defaultWorkspaceNetwork = "workspace_network"
	defaultShareCortex      = "share_cortex"
	defaultShareReference   = "share_reference"
	defaultShareNotes       = "share_notes"
	defaultShareUploads     = "share_uploads"
	defaultInboundMode      = "inbound_mode"
)

// Tri-state values. Empty is the third and is never written: it is what a
// record holds when nobody has decided.
const (
	settingOn  = "on"
	settingOff = "off"
)

// agentWorkspaceNetwork resolves whether code in this agent's workspace may
// open a connection, in the order the answers override each other.
//
// The agent's own answer, then the legacy bool for a record written before
// this existed, then the owner's default, then the framework's: allowed. The
// framework's answer is last and is OPEN, which is what every deployment did
// before any of this and must stay true for one that sets nothing.
func agentWorkspaceNetwork(db Database, rec AgentRecord) bool {
	return settingIsOn(db, rec, defaultWorkspaceNetwork)
}

// workspaceNetworkSource says WHERE that answer came from, for a page that has
// to show an override as an override rather than as a value.
func workspaceNetworkSource(db Database, rec AgentRecord) string {
	return settingSource(db, rec, defaultWorkspaceNetwork)
}


// triSetting describes one setting that can carry a fleet default.
//
// A table rather than a resolver each, because the shape is identical every
// time: the agent's own answer, a record written before the tri-state, the
// owner's default, the framework's. Written out four times it drifts in the
// order or in what an empty value means, and the difference between those is
// whether a fleet blocks something or opens it.
type triSetting struct {
	key string // the fleet-default key, and the record's json field
	// own reads the agent's own tri-state answer: "on", "off" or "".
	own func(AgentRecord) string
	// legacy reads a record written before this setting was tri-state, and
	// reports whether it said anything. Only a value the old field could
	// actually record counts: most were bools that could express one side.
	legacy func(AgentRecord) (string, bool)
	// framework is the answer when nobody has given one. It is what the
	// deployment did before any of this existed and must stay that way for
	// one that sets nothing.
	framework string
	// values are what may be stored, empty aside. Declared per setting
	// because they are not all on and off: inbound reach takes its own modes,
	// and a shared on/off check would refuse them while looking correct.
	values []string
	// words is what each value is CALLED, per setting, because the stored
	// value is not a word anybody can act on: "on" means allowed for the
	// workspace ceiling, shared for a memory layer and permitted for uploads.
	// A page that printed the stored value six times would be six rows of
	// "on" and nothing to choose between them.
	//
	// Declared here so the admin selects, the per-agent selects and the line
	// that says where an answer came from all say the same thing. They said
	// three different things when each built its own.
	words map[string]string
	// strictness orders every value this setting takes from LOOSEST to
	// STRICTEST, which is what lets the deployment ceiling clamp generically.
	//
	// Declared rather than derived, because "stricter" does not run the same
	// way for all of them: for workspace reach off is stricter, for the share
	// layers off is stricter, and for inbound the order runs any, only, none.
	// A ceiling that guessed would clamp half of these the wrong way, and
	// clamping the wrong way is the one failure a ceiling must not have.
	//
	// Includes the empty string where empty is a real value (inbound's "any"),
	// and omits it where empty only means "nobody decided".
	strictness []string
}

// onOff is the common case, named once so a setting that takes it says so
// rather than repeating a literal that could drift.
func onOff() []string { return []string{settingOn, settingOff} }

// sharedOrPrivate names the two sides of a memory layer on a SHARED agent.
//
// Not "they see it" and "kept to yourself", which is what these said and which
// overstates both sides. A recipient's view of a shared agent is already
// narrow, so "they see it" reads as a disclosure it is not; and the off side
// is not withholding, it is the recipient building a layer of their own that
// the owner never reads either. Shared or private is what actually differs.
//
// "Private" here is about this LAYER, not about Private mode on the Network
// tab, which is the turn's network cutoff. They share a word and nothing else.
// In context the word is the plain one - the owner's cortex stays private -
// and inventing a second vocabulary to keep them apart would cost more than
// the collision does.
func sharedOrPrivate() map[string]string {
	return map[string]string{settingOn: "Shared", settingOff: "Private"}
}

// settingWord is what one value of one setting is CALLED. Falls back to the
// stored value, so a value added without a word still renders as something
// rather than as an empty option.
func settingWord(key, value string) string {
	if s, ok := triSettings[key]; ok {
		if w := s.words[value]; w != "" {
			return w
		}
	}
	if value == "" {
		return "not set"
	}
	return value
}

// looseToStrict is the strictness order for an on/off setting. On is the loose
// side of every one of them: the workspace may dial, the person it is shared
// with sees the layer.
func looseToStrict() []string { return []string{settingOn, settingOff} }

var triSettings = map[string]triSetting{
	defaultWorkspaceNetwork: {
		key:   defaultWorkspaceNetwork,
		words: map[string]string{settingOn: "Allowed", settingOff: "Blocked"},
		own: func(a AgentRecord) string { return a.WorkspaceNetwork },
		// The old field could only ever record a BLOCK.
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.WorkspaceNoNetwork },
		framework:  settingOn,
		values:     onOff(),
		strictness: looseToStrict(),
	},
	defaultShareCortex: {
		key:       defaultShareCortex,
		words:     sharedOrPrivate(),
		own:       func(a AgentRecord) string { return a.ShareCortex },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareHoldCortex },
		framework:  settingOn,
		values:     onOff(),
		strictness: looseToStrict(),
	},
	defaultShareReference: {
		key:       defaultShareReference,
		words:     sharedOrPrivate(),
		own:       func(a AgentRecord) string { return a.ShareReference },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareHoldReference },
		framework:  settingOn,
		values:     onOff(),
		strictness: looseToStrict(),
	},
	defaultShareNotes: {
		key:   defaultShareNotes,
		words: sharedOrPrivate(),
		own: func(a AgentRecord) string { return a.ShareNotes },
		// The one legacy flag stored POSITIVELY: it granted rather than
		// withheld, so a true means on and its framework answer is off.
		legacy:    func(a AgentRecord) (string, bool) { return settingOn, a.ShareMemoryExplicit },
		framework:  settingOff,
		values:     onOff(),
		strictness: looseToStrict(),
	},
	defaultShareUploads: {
		key:       defaultShareUploads,
		words:     map[string]string{settingOn: "Allowed", settingOff: "Not allowed"},
		own:       func(a AgentRecord) string { return a.ShareUploads },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareNoUploads },
		framework:  settingOn,
		values:     onOff(),
		strictness: looseToStrict(),
	},
	defaultInboundMode: {
		key: defaultInboundMode,
		words: map[string]string{
			inboundAny: "Anyone", inboundOnly: "Only its named callers", inboundNone: "Nobody"},
		// Not on/off: its values are the inbound modes, and "" already meant
		// "any agent". It carries a default the same way regardless.
		own:       func(a AgentRecord) string { return a.InboundMode },
		legacy:    func(AgentRecord) (string, bool) { return "", false },
		framework: inboundAny,
		values:    []string{inboundOnly, inboundNone},
		// inboundAny is the loosest and is spelled "", which here is a VALUE
		// and not an absence: an agent that has answered nothing accepts
		// anyone, so a ceiling has something to clamp.
		strictness: []string{inboundAny, inboundOnly, inboundNone},
	},
}

// resolveSetting answers one setting for one agent, in the order the answers
// override each other.
func resolveSettingRaw(db Database, rec AgentRecord, key string) string {
	s, ok := triSettings[key]
	if !ok {
		return ""
	}
	if v := strings.TrimSpace(s.own(rec)); v != "" {
		return v
	}
	if v, said := s.legacy(rec); said {
		return v
	}
	if v := deploymentSetting(db, deploymentDefault, s.key); v != "" {
		return v
	}
	return s.framework
}

// resolveSetting answers one setting and applies the deployment's ceiling.
//
// Every reader goes through here. resolveSettingRaw is what the chain alone
// says, kept separate so a page can show an owner what they set beside what
// the deployment allows - a ceiling that silently rewrote the answer would
// leave somebody reading their own control and not believing it.
func resolveSetting(db Database, rec AgentRecord, key string) string {
	return clampToDeploymentMaximum(db, key, resolveSettingRaw(db, rec, key))
}

// settingIsOn is the bool form, for a setting whose values are on and off.
func settingIsOn(db Database, rec AgentRecord, key string) bool {
	return resolveSetting(db, rec, key) == settingOn
}

// settingSource is the whole line that goes under a per-agent control: what
// the setting is, and whether this agent decided it or is following.
//
// It LEADS with which of those it is, because that is what the reader is
// deciding about. The line used to open "Currently " and then name a rung
// ("from the default for all agents: off"), which buried both halves: the
// value arrived last and in its stored spelling, and "currently" is true of
// every value a control has ever shown.
//
// In the setting's own WORDS. "on" means allowed for the workspace ceiling and
// shared for a memory layer, and a line reading "on" tells a reader nothing
// they can act on.
func settingSource(db Database, rec AgentRecord, key string) string {
	s, ok := triSettings[key]
	if !ok {
		return ""
	}
	word := func(v string) string { return settingWord(key, v) }
	// The limit, said wherever one is set. A select missing the option
	// somebody came to pick, with nothing explaining why, reads as a broken
	// control - and the reason is a deployment limit they may not be able to
	// see. Absent where none is set: a line explaining a constraint nobody
	// imposed is noise on every other deployment.
	limit := ""
	if v := effectiveDeploymentMaximum(db, key); v != "" && len(s.strictness) > 0 && v != s.strictness[0] {
		limit = " Limit: " + word(v) + ", so nothing looser can be set here."
	}
	// The agent's OWN answer, which is the only one a limit can be said to
	// override. An agent that has decided nothing is not asking for anything,
	// so reporting it as held would tell somebody their setting was overruled
	// when they never made one - and it would say so on every agent in a
	// deployment that has a limit at all.
	own := strings.TrimSpace(s.own(rec))
	if own == "" {
		if v, said := s.legacy(rec); said {
			own = v
		}
	}
	if own != "" {
		if held := clampToDeploymentMaximum(db, key, own); held != own {
			return "Held at " + word(held) + " by the deployment limit - this agent asks for " + word(own) + "."
		}
		return "Set on this agent: " + word(own) + ". Default: " + word(effectiveDeploymentDefault(db, key)) + "." + limit
	}
	// Following. Named as the DEFAULT rather than as a rung it came from: an
	// owner does not care which store answered, they care that this agent has
	// not decided and will move if the default does.
	return "Default: " + word(effectiveDeploymentDefault(db, key)) + ". This agent has not decided, so it follows." + limit
}
