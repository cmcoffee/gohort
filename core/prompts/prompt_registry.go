package prompts

import "sync"

// PromptBlock is one framework prompt fragment, registered so its text can be
// changed without a release. Whatever assembles a system prompt (e.g.
// orchestrate's capability-gated framework blocks) registers its blocks at
// init(), and the assembler reads each block's effective text: an override
// when one is set, else the in-code default.
type PromptBlock struct {
	Key      string // stable id, e.g. "framework.plan_set"
	Title    string // display heading
	Category string // grouping shown as a section, e.g. "Orchestration"
	Gate     string // human description of when the block applies
	Text     string // the block text as injected
	// Builtin marks a block that shipped with gohort, as opposed to one an
	// operator added. Set by RegisterPromptBlock, so a code-registered block is
	// always builtin and only the custom-block store can produce a false. It
	// decides what a surface may offer: a builtin can be disabled (reversible,
	// its shipped text stays on the page), a custom one can be deleted outright.
	Builtin bool
}

var (
	promptBlockMu sync.Mutex
	promptBlocks  []PromptBlock
)

// RegisterPromptBlock adds a block to the Prompts registry. Call once per block,
// typically from an init() co-located with the text it surfaces.
func RegisterPromptBlock(b PromptBlock) {
	b.Builtin = true // registered from code, by definition
	promptBlockMu.Lock()
	defer promptBlockMu.Unlock()
	promptBlocks = append(promptBlocks, b)
}

// upsertPromptBlock registers b, or replaces the block with its key when
// that block's text, title or category differs. Reports whether anything
// changed.
func upsertPromptBlock(b PromptBlock) bool {
	b.Builtin = true
	promptBlockMu.Lock()
	defer promptBlockMu.Unlock()
	for i, have := range promptBlocks {
		if have.Key != b.Key {
			continue
		}
		if have.Text == b.Text && have.Title == b.Title && have.Category == b.Category {
			return false
		}
		promptBlocks[i] = b
		return true
	}
	promptBlocks = append(promptBlocks, b)
	return true
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
// A block's in-code text is the DEFAULT; an override replaces it. Overrides are
// written by the tuning harness (Optimize) and the admin Style rules, and live
// in the main DB's OverrideTable (deployment-level, like tunables) keyed by
// block Key. The prompt assembler reads the EFFECTIVE text (override-or-default),
// so an override changes what agents actually receive. Reversible: clearing the
// override restores the default.

const promptOverridePrefix = "prompt_override."

var (
	promptOverrideMu sync.Mutex
	promptOverrideDB Store
)

// SetPromptOverrideDB wires the DB that holds operator prompt-block overrides.
// Call once at startup, mirroring SetTunablesDB.
func SetPromptOverrideDB(db Store) {
	promptOverrideMu.Lock()
	promptOverrideDB = db
	promptOverrideMu.Unlock()
	if db != nil {
		loadObservedTools(db, tunableToolNames())
	}
	tierTextChanged()
}

func promptOverrideStore() Store {
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
	if db.Get(OverrideTable, promptOverridePrefix+key, &s) && s != "" {
		return s, true
	}
	return "", false
}

// SetPromptOverride stores an operator override for a block key.
func SetPromptOverride(key, text string) {
	if db := promptOverrideStore(); db != nil {
		db.Set(OverrideTable, promptOverridePrefix+key, text)
	}
	tierTextChanged()
}

// ClearPromptOverride removes an operator override, restoring the default.
func ClearPromptOverride(key string) {
	if db := promptOverrideStore(); db != nil {
		db.Unset(OverrideTable, promptOverridePrefix+key)
	}
	tierTextChanged()
}

// EffectivePromptText returns the operator override for a block key when one is
// set, else def (the in-code default). This is what the prompt assembler
// injects, so an override changes the text agents receive.
func EffectivePromptText(key, def string) string {
	if s, ok := PromptOverride(key); ok {
		return s
	}
	return def
}
