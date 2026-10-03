package prompts

// Tool descriptions as prompt blocks.
//
// What decides how an agent builds is not only its prompt: it is the
// descriptions of the tools it builds with, which say when to reach for a
// machine and when for a pipeline, what a tool definition needs before it is
// done. Those lived in code, out of reach of the Prompts page, the tuning
// harness and per-tier wording. A shipped tool named here becomes a block
// keyed "tool.<name>": its text is the description the code ships, seen the
// first time the tool goes out to a model, and remembered so the block is
// there after a restart. An edit to the block, for every tier or for one,
// replaces the description in every call the tool goes out with.
//
// Only tools named here, never a user's own: their descriptions are theirs,
// and a description built from someone's data does not belong in a
// deployment-wide block.

import (
	"strings"
	"sync"
)

// ToolBlockPrefix starts the key of a tool description's block.
const ToolBlockPrefix = "tool."

// ToolBlockKey is the block key for a tool's description.
func ToolBlockKey(name string) string { return ToolBlockPrefix + name }

// Tool groups, shown as the blocks' categories.
const (
	ToolGroupAuthoring = "Tools: authoring"
	ToolGroupFramework = "Tools: framework"
)

var (
	tunableToolsMu sync.RWMutex
	tunableTools   = map[string]string{} // tool name -> group
)

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
	if unstableTools[name] {
		return ""
	}
	return tunableTools[name]
}

// unstableTools are tools whose description changed too often to be one
// text: built per caller or from the caller's data. An edit would replace
// whatever that caller was meant to read, so they are left as the code
// builds them.
var unstableTools = map[string]bool{}

// unstableAfter is how many changes of one tool's description in one process
// make it unstable. A deploy that changes it is one.
const unstableAfter = 3

// observedToolPrefix keeps the shipped description of each tool seen, so its
// block survives a restart.
const observedToolPrefix = "prompt_tool_shipped."

// ObserveToolDescription records the description a tool ships with as its
// block's text: registering the block the first time the tool is seen, and
// following the code when the shipped description changes. desc is what
// the code built, before any edit is applied.
func ObserveToolDescription(name, desc string) {
	group := TunableToolGroup(name)
	if group == "" || strings.TrimSpace(desc) == "" {
		return
	}
	// Seen as it is: the common case, every call, kept to a map read.
	observedMu.Lock()
	prev, seen := observed[name]
	same := prev == desc
	observed[name] = desc
	if seen && !same {
		observedChanges[name]++
	}
	flipping := observedChanges[name] >= unstableAfter
	observedMu.Unlock()
	if same {
		return
	}
	if flipping {
		tunableToolsMu.Lock()
		first := !unstableTools[name]
		unstableTools[name] = true
		tunableToolsMu.Unlock()
		if first {
			logUnstableTool(name)
			tierTextChanged()
		}
		return
	}
	b := PromptBlock{Key: ToolBlockKey(name), Title: name, Category: group,
		Gate: "Every call that offers the " + name + " tool.", Text: desc}
	if !upsertPromptBlock(b) {
		return
	}
	if db := promptOverrideStore(); db != nil {
		db.Set(OverrideTable, observedToolPrefix+name, b)
	}
	tierTextChanged()
}

var (
	observedMu      sync.Mutex
	observed        = map[string]string{}
	observedChanges = map[string]int{}
	// logUnstableTool says a tool left the editable set; core's log, set
	// by core (this package does not import it).
	logUnstableTool = func(name string) {}
)

// SetUnstableToolLogger sets what is told when a tool's description proves
// to vary per call and stops being editable.
func SetUnstableToolLogger(fn func(name string)) {
	if fn != nil {
		logUnstableTool = fn
	}
}

// loadObservedTools registers the tool blocks seen before this process
// started.
func loadObservedTools(db Store, names []string) {
	for _, name := range names {
		var b PromptBlock
		if db.Get(OverrideTable, observedToolPrefix+name, &b) && b.Key != "" {
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
	e := toolEdits(name)
	if t, ok := e.tier[tier]; ok {
		return t
	}
	if e.hasAll {
		return e.all
	}
	return desc
}

// toolEdit is a tool description's edits, read once per change to any
// prompt text: a call offers dozens of tools, and reading the store for
// each one on every call is a cost paid for edits that almost never exist.
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

func toolEdits(name string) toolEdit {
	tierMu.Lock()
	gen := tierGen
	tierMu.Unlock()
	toolEditMu.Lock()
	if toolEditGen != gen {
		toolEditMap, toolEditGen = map[string]toolEdit{}, gen
	}
	e, ok := toolEditMap[name]
	toolEditMu.Unlock()
	if ok {
		return e
	}
	key := ToolBlockKey(name)
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
		toolEditMap[name] = e
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
