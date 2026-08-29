// Turning a rule OFF, and adding one.
//
// The text override in prompt_registry.go answers "say it differently". Two
// things it cannot do are "stop saying it at all" and "also say this", and
// both are add/remove on a list rather than an edit of a string. That is the
// shape the markdown extension registry already uses: a base set that ships,
// plus what an operator puts on top.
//
// WHY A RULE IS NOT JUST ITS TEXT. A house-style rule can have two halves: the
// sentence that asks the model, and the transform that guarantees it. The
// em-dash rule is both — the [Style:] clause asks, and textutil.StripEmDashes
// enforces at the delivery boundary. Held apart, turning the rule "off" would
// delete the sentence and leave the transform stripping characters nobody asked
// it to, or the reverse: the prompt still demanding something the code no
// longer does. So an enforcer BINDS to a block key, and disabling the block
// stops both halves at once.
//
// Adds are text-only by construction: an operator cannot supply a Go function.
// That is worth stating on the page rather than hiding, because it is the
// honest difference between a rule that asks and a rule that holds.
//
// Deployment-wide, like the text overrides and the tunables they mirror. The
// store is keyed so a per-agent scope can be layered on later without moving
// anything.

package prompts

import "sync"

const (
	// promptDisabledPrefix keys the per-block off switch.
	promptDisabledPrefix = "prompt_disabled."
	// customBlocksKey holds operator-added blocks as one list.
	customBlocksKey = "prompt_custom_blocks"
)

// PromptBlockEnabled reports whether a block is live. Unset means ON: a rule
// ships enabled, and only an explicit operator action turns it off, so a fresh
// deployment behaves exactly as the code says it does.
func PromptBlockEnabled(key string) bool {
	db := promptOverrideStore()
	if db == nil {
		return true
	}
	var off bool
	if db.Get(OverrideTable, promptDisabledPrefix+key, &off) {
		return !off
	}
	return true
}

// SetPromptBlockEnabled turns a block on or off deployment-wide.
func SetPromptBlockEnabled(key string, enabled bool) {
	db := promptOverrideStore()
	if db == nil {
		return
	}
	if enabled {
		db.Unset(OverrideTable, promptDisabledPrefix+key)
		return
	}
	db.Set(OverrideTable, promptDisabledPrefix+key, true)
}

// EffectiveBlockText is EffectivePromptText plus the off switch: a disabled
// block yields "", which every assembler treats as nothing to append. Prefer
// this at an injection site so one call answers both "is it on" and "what does
// it say".
func EffectiveBlockText(key, def string) string {
	if !PromptBlockEnabled(key) {
		return ""
	}
	return EffectivePromptText(key, def)
}

// --- operator-added blocks ---------------------------------------------------

// CustomPromptBlocks returns the operator's own blocks. They carry Builtin=false
// so a surface can show plainly which rules shipped and which were added here.
func CustomPromptBlocks() []PromptBlock {
	db := promptOverrideStore()
	if db == nil {
		return nil
	}
	var out []PromptBlock
	db.Get(OverrideTable, customBlocksKey, &out)
	return out
}

// AddCustomPromptBlock stores an operator-authored block, replacing any existing
// one with the same key. Enforce is not a field an operator can fill, so a
// custom block only ever asks.
func AddCustomPromptBlock(b PromptBlock) {
	db := promptOverrideStore()
	if db == nil || b.Key == "" {
		return
	}
	b.Builtin = false
	blocks := CustomPromptBlocks()
	for i := range blocks {
		if blocks[i].Key == b.Key {
			blocks[i] = b
			db.Set(OverrideTable, customBlocksKey, blocks)
			return
		}
	}
	db.Set(OverrideTable, customBlocksKey, append(blocks, b))
}

// RemoveCustomPromptBlock deletes an operator-authored block. A BUILTIN is not
// removable this way and must not be: disabling one is reversible and leaves
// the shipped text on the page to turn back on, where deleting it would lose
// the incident each builtin encodes.
func RemoveCustomPromptBlock(key string) {
	db := promptOverrideStore()
	if db == nil {
		return
	}
	blocks := CustomPromptBlocks()
	out := blocks[:0]
	for _, b := range blocks {
		if b.Key != key {
			out = append(out, b)
		}
	}
	db.Set(OverrideTable, customBlocksKey, out)
}

// --- enforcers ---------------------------------------------------------------

var (
	enforcerMu sync.Mutex
	enforcers  []ruleEnforcer
)

type ruleEnforcer struct {
	key string
	fn  func(string) string
}

// RegisterRuleEnforcer binds a deterministic transform to a block key. Call it
// beside the block's registration, so the sentence and the guarantee are
// declared together and cannot drift apart.
func RegisterRuleEnforcer(key string, fn func(string) string) {
	if key == "" || fn == nil {
		return
	}
	enforcerMu.Lock()
	enforcers = append(enforcers, ruleEnforcer{key: key, fn: fn})
	enforcerMu.Unlock()
}

// ApplyRuleEnforcers runs every ENABLED rule's transform over user-facing text,
// in registration order. This is the delivery boundary's single call: adding a
// rule with an enforcer makes it apply everywhere at once, and disabling that
// rule stops it everywhere at once.
func ApplyRuleEnforcers(s string) string {
	if s == "" {
		return s
	}
	enforcerMu.Lock()
	list := make([]ruleEnforcer, len(enforcers))
	copy(list, enforcers)
	enforcerMu.Unlock()
	for _, e := range list {
		if PromptBlockEnabled(e.key) {
			s = e.fn(s)
		}
	}
	return s
}

// RuleEnforcerKeys lists the block keys that carry an enforcer, so a surface can
// mark which rules actually hold versus which only ask.
func RuleEnforcerKeys() []string {
	enforcerMu.Lock()
	defer enforcerMu.Unlock()
	out := make([]string, 0, len(enforcers))
	for _, e := range enforcers {
		out = append(out, e.key)
	}
	return out
}
