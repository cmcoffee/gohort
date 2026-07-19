package core

import "sync"

// PromptBlock is one operator-visible framework prompt fragment surfaced on the
// Prompts page. Whatever assembles a system prompt (e.g. orchestrate's
// capability-gated framework blocks) registers its blocks at init(), so the
// Prompts surface can show the otherwise-hidden text that shapes agent
// behavior. Read-only for now — this is the "make the hidden prompts visible"
// step; editing/toggling (the RuleSet policy) layers on later, at which point
// this registry becomes the source the assembler reads instead of in-code
// constants.
type PromptBlock struct {
	Key      string // stable id, e.g. "framework.plan_set"
	Title    string // display heading
	Category string // grouping shown as a section, e.g. "Orchestration"
	Gate     string // human description of when the block applies
	Text     string // the block text as injected
}

var (
	promptBlockMu sync.Mutex
	promptBlocks  []PromptBlock
)

// RegisterPromptBlock adds a block to the Prompts registry. Call once per block,
// typically from an init() co-located with the text it surfaces.
func RegisterPromptBlock(b PromptBlock) {
	promptBlockMu.Lock()
	defer promptBlockMu.Unlock()
	promptBlocks = append(promptBlocks, b)
}

// AllPromptBlocks returns a copy of the registered blocks in registration order.
func AllPromptBlocks() []PromptBlock {
	promptBlockMu.Lock()
	defer promptBlockMu.Unlock()
	out := make([]PromptBlock, len(promptBlocks))
	copy(out, promptBlocks)
	return out
}

// --- operator overrides ------------------------------------------------------
//
// A block's in-code text is the DEFAULT; an operator can override it on the
// Prompts page. Overrides live in the main DB's WebTable (deployment-level, like
// tunables) keyed by block Key, and the prompt assembler reads the EFFECTIVE
// text (override-or-default) — so an edit changes what agents actually receive.
// Reversible: clearing the override restores the default.

const promptOverridePrefix = "prompt_override."

var (
	promptOverrideMu sync.Mutex
	promptOverrideDB Database
)

// SetPromptOverrideDB wires the DB that holds operator prompt-block overrides.
// Call once at startup, mirroring SetTunablesDB.
func SetPromptOverrideDB(db Database) {
	promptOverrideMu.Lock()
	promptOverrideDB = db
	promptOverrideMu.Unlock()
}

func promptOverrideStore() Database {
	promptOverrideMu.Lock()
	defer promptOverrideMu.Unlock()
	return promptOverrideDB
}

// PromptOverride returns the operator override text for a block key, if set.
func PromptOverride(key string) (string, bool) {
	db := promptOverrideStore()
	if db == nil {
		return "", false
	}
	var s string
	if db.Get(WebTable, promptOverridePrefix+key, &s) && s != "" {
		return s, true
	}
	return "", false
}

// SetPromptOverride stores an operator override for a block key.
func SetPromptOverride(key, text string) {
	if db := promptOverrideStore(); db != nil {
		db.Set(WebTable, promptOverridePrefix+key, text)
	}
}

// ClearPromptOverride removes an operator override, restoring the default.
func ClearPromptOverride(key string) {
	if db := promptOverrideStore(); db != nil {
		db.Unset(WebTable, promptOverridePrefix+key)
	}
}

// EffectivePromptText returns the operator override for a block key when one is
// set, else def (the in-code default). This is what the prompt assembler
// injects, so an edit on the Prompts page changes the text agents receive.
func EffectivePromptText(key, def string) string {
	if s, ok := PromptOverride(key); ok {
		return s
	}
	return def
}
