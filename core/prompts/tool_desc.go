package prompts

// Tool and parameter descriptions as prompt blocks.
//
// What decides how an agent builds is not only its prompt: it is the
// descriptions of the tools it builds with, which say when to reach for a
// machine and when for a pipeline, and the descriptions of their parameters,
// which carry most of the how (what an app's sections may hold, how a
// machine's phases hand off). Those lived in code, out of reach of
// overrides, per-model wording and Optimize. A shipped tool named here
// becomes a block keyed "tool.<name>", and each of its parameters one keyed
// "tool.<name>.<param>": the text is the description the code ships, seen
// the first time the tool goes out to a model, and remembered so the block is
// there after a restart. An edit, for every model or one, replaces the
// description in every call the tool goes out with.
//
// Only tools named here, never a user's own: their descriptions are theirs,
// and a description built from someone's data does not belong in a
// deployment-wide block. Top-level parameters only; a nested property's
// description is left as the code builds it.

import (
	"strings"
	"sync"
)

// ToolBlockPrefix starts the key of a tool's or a parameter's block.
const ToolBlockPrefix = "tool."

// ToolBlockKey is the block key for a tool's description.
func ToolBlockKey(name string) string { return ToolBlockPrefix + name }

// ToolParamBlockKey is the block key for one of a tool's parameters.
func ToolParamBlockKey(tool, param string) string { return ToolBlockPrefix + tool + "." + param }

// Tool groups, shown as the blocks' categories.
const (
	ToolGroupAuthoring = "Tools: authoring"
	ToolGroupFramework = "Tools: framework"
)

var (
	tunableToolsMu sync.RWMutex
	tunableTools   = map[string]string{} // tool name -> group
	// unstable are blocks whose shipped text changed too often to be one
	// text: built per caller or from the caller's data. An edit would replace
	// whatever that caller was meant to read, so they are left as the code
	// builds them.
	unstable = map[string]bool{} // block key -> true
)

// unstableAfter is how many changes of one description in one process make
// it unstable. A deploy that changes it is one.
const unstableAfter = 3

// RegisterTunableTools names shipped tools whose descriptions are blocks.
// Call from an init().
func RegisterTunableTools(group string, names ...string) {
	tunableToolsMu.Lock()
	defer tunableToolsMu.Unlock()
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			tunableTools[n] = group
		}
	}
}

// TunableToolGroup is the group a tool's description belongs to, or "" when
// it is not a block.
func TunableToolGroup(name string) string {
	tunableToolsMu.RLock()
	defer tunableToolsMu.RUnlock()
	if unstable[ToolBlockKey(name)] {
		return ""
	}
	return tunableTools[name]
}

// tunableParam is the group of a tool's parameter block, or "" when it is
// not one: the tool is not named, or the parameter's text proved unstable.
func tunableParam(tool, param string) string {
	tunableToolsMu.RLock()
	defer tunableToolsMu.RUnlock()
	if unstable[ToolParamBlockKey(tool, param)] {
		return ""
	}
	return tunableTools[tool]
}

// observedToolPrefix keeps the shipped description of each block seen, and
// observedIndexKey the list of them, so the blocks survive a restart.
const (
	observedToolPrefix = "prompt_tool_shipped."
	observedIndexKey   = "prompt_tool_shipped_index"
)

var (
	observedMu      sync.Mutex
	observed        = map[string]string{} // block key -> last text seen
	observedChanges = map[string]int{}
	// logUnstableTool says a description left the editable set; core's
	// log, set by core (this package does not import it).
	logUnstableTool = func(name string) {}
)

// SetUnstableToolLogger sets what is told when a description proves to vary
// per call and stops being editable.
func SetUnstableToolLogger(fn func(name string)) {
	if fn != nil {
		logUnstableTool = fn
	}
}

// ObserveToolDescription records the description a tool ships with as its
// block's text: registering the block the first time the tool is seen, and
// following the code when the shipped description changes. desc is what
// the code built, before any edit is applied.
func ObserveToolDescription(name, desc string) {
	group := TunableToolGroup(name)
	if group == "" {
		return
	}
	observeBlock(PromptBlock{Key: ToolBlockKey(name), Title: name, Category: group,
		Gate: "Every call that offers the " + name + " tool.", Text: desc}, name)
}

// ObserveToolParamDescription is ObserveToolDescription for one of a tool's
// parameters.
func ObserveToolParamDescription(tool, param, desc string) {
	group := tunableParam(tool, param)
	if group == "" {
		return
	}
	observeBlock(PromptBlock{Key: ToolParamBlockKey(tool, param), Title: tool + ": " + param, Category: group,
		Gate: "The " + param + " parameter, in every call that offers the " + tool + " tool.", Text: desc}, tool+"."+param)
}

func observeBlock(b PromptBlock, name string) {
	if strings.TrimSpace(b.Text) == "" {
		return
	}
	// Seen as it is: the common case, every call, kept to a map read.
	observedMu.Lock()
	prev, seen := observed[b.Key]
	same := prev == b.Text
	observed[b.Key] = b.Text
	if seen && !same {
		observedChanges[b.Key]++
	}
	flipping := observedChanges[b.Key] >= unstableAfter
	observedMu.Unlock()
	if same {
		return
	}
	if flipping {
		tunableToolsMu.Lock()
		first := !unstable[b.Key]
		unstable[b.Key] = true
		tunableToolsMu.Unlock()
		if first {
			logUnstableTool(name)
			tierTextChanged()
		}
		return
	}
	if !upsertPromptBlock(b) {
		return
	}
	if db := promptOverrideStore(); db != nil {
		db.Set(OverrideTable, observedToolPrefix+strings.TrimPrefix(b.Key, ToolBlockPrefix), b)
		var index []string
		db.Get(OverrideTable, observedIndexKey, &index)
		if !tierHas(index, b.Key) {
			db.Set(OverrideTable, observedIndexKey, append(index, b.Key))
		}
	}
	tierTextChanged()
}

// loadObservedTools registers the blocks seen before this process started.
func loadObservedTools(db Store, names []string) {
	keys := map[string]bool{}
	var index []string
	db.Get(OverrideTable, observedIndexKey, &index)
	for _, k := range index {
		keys[k] = true
	}
	for _, n := range names {
		keys[ToolBlockKey(n)] = true // kept before there was an index
	}
	for k := range keys {
		var b PromptBlock
		if db.Get(OverrideTable, observedToolPrefix+strings.TrimPrefix(k, ToolBlockPrefix), &b) && b.Key != "" {
			upsertPromptBlock(b)
		}
	}
}

// ToolDescriptionFor is what a tool's description says in a call the tier
// answers: the tier's own text, else the all-tiers edit, else desc. A tool
// not named tunable is desc.
func ToolDescriptionFor(tier, name, desc string) string {
	if TunableToolGroup(name) == "" {
		return desc
	}
	return editedText(tier, ToolBlockKey(name), desc)
}

// ToolParamDescriptionFor is ToolDescriptionFor for one of a tool's
// parameters.
func ToolParamDescriptionFor(tier, tool, param, desc string) string {
	if tunableParam(tool, param) == "" {
		return desc
	}
	return editedText(tier, ToolParamBlockKey(tool, param), desc)
}

func editedText(tier, key, desc string) string {
	e := toolEdits(key)
	if t, ok := e.tier[tier]; ok {
		return t
	}
	if e.hasAll {
		return e.all
	}
	return desc
}

// toolEdit is a block's edits, read once per change to any prompt text: a
// call offers dozens of tools and hundreds of parameters, and reading the
// store for each on every call is a cost paid for edits that almost never
// exist.
type toolEdit struct {
	all    string
	hasAll bool
	tier   map[string]string
}

var (
	toolEditMu  sync.Mutex
	toolEditGen uint64
	toolEditMap = map[string]toolEdit{}
)

func toolEdits(key string) toolEdit {
	tierMu.Lock()
	gen := tierGen
	tierMu.Unlock()
	toolEditMu.Lock()
	if toolEditGen != gen {
		toolEditMap, toolEditGen = map[string]toolEdit{}, gen
	}
	e, ok := toolEditMap[key]
	toolEditMu.Unlock()
	if ok {
		return e
	}
	e.all, e.hasAll = PromptOverride(key)
	for _, t := range Tiers() {
		if o, ok := PromptTierOverride(t, key); ok {
			if e.tier == nil {
				e.tier = map[string]string{}
			}
			e.tier[t] = o.Text
		}
	}
	toolEditMu.Lock()
	if toolEditGen == gen {
		toolEditMap[key] = e
	}
	toolEditMu.Unlock()
	return e
}

// tunableToolNames lists every tool named tunable.
func tunableToolNames() []string {
	tunableToolsMu.RLock()
	defer tunableToolsMu.RUnlock()
	out := make([]string, 0, len(tunableTools))
	for n := range tunableTools {
		out = append(out, n)
	}
	return out
}
