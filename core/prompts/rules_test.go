package prompts

import "testing"

// memStore is the smallest thing satisfying Store, so these tests exercise the
// real read/write paths rather than a mocked decision.
type memStore map[string]interface{}

func (m memStore) Get(table, key string, out interface{}) bool {
	v, ok := m[table+"|"+key]
	if !ok {
		return false
	}
	switch p := out.(type) {
	case *string:
		s, _ := v.(string)
		*p = s
	case *bool:
		b, _ := v.(bool)
		*p = b
	case *[]PromptBlock:
		bs, _ := v.([]PromptBlock)
		*p = bs
	default:
		return false
	}
	return true
}
func (m memStore) Set(table, key string, value interface{}) { m[table+"|"+key] = value }
func (m memStore) Unset(table, key string)                  { delete(m, table+"|"+key) }

func withStore(t *testing.T) memStore {
	t.Helper()
	prev := promptOverrideStore()
	db := memStore{}
	SetPromptOverrideDB(db)
	t.Cleanup(func() { SetPromptOverrideDB(prev) })
	return db
}

// A rule ships ON. Only an explicit operator action turns one off, so a fresh
// deployment behaves exactly as the code reads.
func TestBlocksAreEnabledUntilTurnedOff(t *testing.T) {
	withStore(t)
	if !PromptBlockEnabled("style.no_em_dash") {
		t.Fatal("a block with no stored state should be live")
	}
	SetPromptBlockEnabled("style.no_em_dash", false)
	if PromptBlockEnabled("style.no_em_dash") {
		t.Error("disabling did not take")
	}
	SetPromptBlockEnabled("style.no_em_dash", true)
	if !PromptBlockEnabled("style.no_em_dash") {
		t.Error("re-enabling did not restore it")
	}
}

// With no DB wired at all, everything is on: an unconfigured deployment must
// not silently lose its rules.
func TestNoStoreMeansEverythingOn(t *testing.T) {
	prev := promptOverrideStore()
	SetPromptOverrideDB(nil)
	t.Cleanup(func() { SetPromptOverrideDB(prev) })
	if !PromptBlockEnabled("anything") {
		t.Fatal("no store should mean enabled")
	}
	if got := EffectiveBlockText("anything", "DEF"); got != "DEF" {
		t.Errorf("EffectiveBlockText = %q, want the default", got)
	}
}

// A disabled block yields no text, which is what makes an assembler omit it
// without every injection site growing its own conditional.
func TestEffectiveBlockTextRespectsBothOverrideAndSwitch(t *testing.T) {
	withStore(t)
	const key = "test.block"

	if got := EffectiveBlockText(key, "DEF"); got != "DEF" {
		t.Errorf("default: %q", got)
	}
	SetPromptOverride(key, "CUSTOM")
	if got := EffectiveBlockText(key, "DEF"); got != "CUSTOM" {
		t.Errorf("override: %q", got)
	}
	// Off beats an override: the operator said stop saying it.
	SetPromptBlockEnabled(key, false)
	if got := EffectiveBlockText(key, "DEF"); got != "" {
		t.Errorf("disabled block still produced %q", got)
	}
	SetPromptBlockEnabled(key, true)
	if got := EffectiveBlockText(key, "DEF"); got != "CUSTOM" {
		t.Errorf("re-enabled should restore the override, got %q", got)
	}
}

func TestCustomBlocksAddAndRemove(t *testing.T) {
	withStore(t)
	AddCustomPromptBlock(PromptBlock{Key: "op.house", Title: "House rule", Text: "Be brief."})
	got := CustomPromptBlocks()
	if len(got) != 1 || got[0].Key != "op.house" {
		t.Fatalf("blocks = %+v", got)
	}
	// An operator cannot author an enforcer, so a custom block only ever asks.
	if got[0].Builtin {
		t.Error("an operator-added block must not claim to be builtin")
	}
	// Same key replaces rather than duplicates.
	AddCustomPromptBlock(PromptBlock{Key: "op.house", Title: "House rule", Text: "Be briefer."})
	if got = CustomPromptBlocks(); len(got) != 1 || got[0].Text != "Be briefer." {
		t.Fatalf("re-add should replace: %+v", got)
	}
	RemoveCustomPromptBlock("op.house")
	if got = CustomPromptBlocks(); len(got) != 0 {
		t.Fatalf("remove left %+v", got)
	}
}

// The point of binding an enforcer to a key: turning the rule off stops the
// transform too, so the prompt and the code can never disagree about whether a
// rule is in force.
func TestEnforcersFollowTheirBlocksSwitch(t *testing.T) {
	withStore(t)
	const key = "test.shout"
	RegisterRuleEnforcer(key, func(s string) string { return s + "!" })

	if got := ApplyRuleEnforcers("hi"); got != "hi!" {
		t.Fatalf("enabled enforcer did not run: %q", got)
	}
	SetPromptBlockEnabled(key, false)
	if got := ApplyRuleEnforcers("hi"); got != "hi" {
		t.Errorf("disabled rule still transformed: %q", got)
	}
	SetPromptBlockEnabled(key, true)
	if got := ApplyRuleEnforcers("hi"); got != "hi!" {
		t.Errorf("re-enabled rule did not run: %q", got)
	}
	if got := ApplyRuleEnforcers(""); got != "" {
		t.Errorf("empty in, empty out; got %q", got)
	}
}
