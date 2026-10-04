package prompts

// Putting a tier's own text into the prompt that tier is actually sent.
//
// Prompts are assembled before the tier is final. A lead call can still end
// up on the worker: the lead is denied, a route stage says worker, the lead
// errors or comes back empty, a loop de-escalates, a forced final answer goes
// to the worker. Every one of those reuses the prompt already built. So the
// choice is not made where the prompt is assembled; it is made on the way
// out, by the one handle every call passes through, once it knows which tier
// is answering. The assembler keeps writing the shared text (the all-tiers
// override, else what shipped), and ApplyTierText swaps each block that has a
// text of its own for the serving tier.
//
// A block written with placeholders ({rounds}, Builder's {{tool_list}}) is
// matched with whatever those placeholders were filled with, and the tier's
// text gets the same values. A swap that cannot be made exactly (the shared
// text is not in the prompt, or the tier's text names a placeholder the
// shared one does not have) leaves the shared text in place: a tier never
// gets less than every tier gets.

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// tierPlaceholderRe matches a placeholder in block text: {{name}} or {name}.
var tierPlaceholderRe = regexp.MustCompile(`\{\{[A-Za-z0-9_.-]+\}\}|\{[A-Za-z0-9_]+\}`)

// tierEdgeMin is how much plain text a block with placeholders must start and
// end with to be found in a prompt. A placeholder at either edge has nothing
// to say where its value starts or stops.
const tierEdgeMin = 12

// tierSwap is one block's swap for one tier.
type tierSwap struct {
	key    string
	shared string
	own    string
	// render, when the block registered one, turns either text into what an
	// assembler puts in a prompt; the swap is then made on the rendered
	// texts, rendered at the time of the call.
	render func(string) string
	// match is set when shared has placeholders: their values are captured
	// in order and names says which is which.
	match *regexp.Regexp
	names []string
}

var (
	tierRenders = map[string]func(string) string{}

	tierMu      sync.Mutex
	tierGen     uint64 // bumped by every write that can change a swap
	tierBuiltAt uint64
	tierBuiltN  int // blocks registered when the cache was built
	tierSwaps   map[string][]tierSwap
	tierApplied = map[string]time.Time{}
)

// tierTextChanged drops the cached swaps. Called by every write to a block's
// text, all-tiers or per tier.
func tierTextChanged() {
	tierMu.Lock()
	tierGen++
	tierMu.Unlock()
}

// RegisterTierRender says how a block's text reaches a prompt when it is not
// put there as written: Builder's {{placeholders}} are expanded first, and
// the expansion of one at the very end has nothing after it to be found by.
// The swap then compares the rendered texts. Call from an init().
func RegisterTierRender(key string, render func(string) string) {
	tierMu.Lock()
	tierRenders[key] = render
	tierGen++
	tierMu.Unlock()
}

// TierPlaceholderProblem says why own cannot stand in for shared, or "" when
// it can: every placeholder own uses must be one shared has, or there would
// be no value to fill it with.
func TierPlaceholderProblem(shared, own string) string {
	have := map[string]bool{}
	for _, p := range tierPlaceholderRe.FindAllString(shared, -1) {
		have[p] = true
	}
	var missing []string
	for _, p := range tierPlaceholderRe.FindAllString(own, -1) {
		if !have[p] && !tierHas(missing, p) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "uses " + strings.Join(missing, ", ") + ", which the block's text does not have, so there is nothing to fill it with"
}

func tierHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// newTierSwap builds the swap from shared to own, or false when it cannot be
// made exactly. render is the block's registered renderer, or nil.
func newTierSwap(key, shared, own string, render func(string) string) (tierSwap, bool) {
	if strings.TrimSpace(shared) == "" || shared == own || TierPlaceholderProblem(shared, own) != "" {
		return tierSwap{}, false
	}
	s := tierSwap{key: key, shared: shared, own: own, render: render}
	if render != nil {
		return s, true
	}
	locs := tierPlaceholderRe.FindAllStringIndex(shared, -1)
	if len(locs) == 0 {
		return s, true
	}
	if locs[0][0] < tierEdgeMin || len(shared)-locs[len(locs)-1][1] < tierEdgeMin {
		return tierSwap{}, false
	}
	var pat strings.Builder
	pat.WriteString("(?s)")
	at := 0
	for i, l := range locs {
		if i > 0 && l[0] == at {
			return tierSwap{}, false // two placeholders side by side
		}
		pat.WriteString(regexp.QuoteMeta(shared[at:l[0]]))
		pat.WriteString("(.*?)")
		s.names = append(s.names, shared[l[0]:l[1]])
		at = l[1]
	}
	pat.WriteString(regexp.QuoteMeta(shared[at:]))
	re, err := regexp.Compile(pat.String())
	if err != nil {
		return tierSwap{}, false
	}
	s.match = re
	return s, true
}

// apply swaps the block in system, or reports false when it is not there.
func (s tierSwap) apply(system string) (string, bool) {
	if s.render != nil {
		shared, own := s.render(s.shared), s.render(s.own)
		if strings.TrimSpace(shared) == "" {
			return system, false
		}
		return swapFirst(system, shared, own)
	}
	if s.match == nil {
		return swapFirst(system, s.shared, s.own)
	}
	m := s.match.FindStringSubmatchIndex(system)
	if m == nil {
		return system, false
	}
	values := map[string]string{}
	for i, name := range s.names {
		if _, seen := values[name]; !seen {
			values[name] = system[m[2+2*i]:m[3+2*i]]
		}
	}
	own := tierPlaceholderRe.ReplaceAllStringFunc(s.own, func(p string) string { return values[p] })
	return system[:m[0]] + own + system[m[1]:], true
}

func swapFirst(system, shared, own string) (string, bool) {
	i := strings.Index(system, shared)
	if i < 0 {
		return system, false
	}
	return system[:i] + own + system[i+len(shared):], true
}

// TierTextPlaceable says why a block's text could not be found in a prompt
// to be swapped for a tier's own, or "" when it can.
func TierTextPlaceable(key, text string) string {
	if strings.HasPrefix(key, ToolBlockPrefix) {
		return "" // replaced whole, by the tool's name (tool_desc.go)
	}
	tierMu.Lock()
	render := tierRenders[key]
	tierMu.Unlock()
	if _, ok := newTierSwap(key, text, text+" ", render); !ok {
		return "its text starts or ends with a placeholder, or has two side by side, so where it sits in a prompt cannot be found"
	}
	return ""
}

// swapsFor returns the tier's swaps, rebuilding them after any write.
func swapsFor(tier string) []tierSwap {
	blocks := AllPromptBlocks()
	tierMu.Lock()
	fresh := tierSwaps != nil && tierBuiltAt == tierGen && tierBuiltN == len(blocks)
	if fresh {
		out := tierSwaps[tier]
		tierMu.Unlock()
		return out
	}
	gen := tierGen
	renders := map[string]func(string) string{}
	for k, fn := range tierRenders {
		renders[k] = fn
	}
	tierMu.Unlock()

	built := map[string][]tierSwap{}
	for _, b := range blocks {
		// A governing peer's wording is what every tier reads: a tier's own
		// text left on this machine does not get to shadow it.
		if _, peer := peerOverride(b.Key); peer {
			continue
		}
		for _, t := range Tiers() {
			o, ok := PromptTierOverride(t, b.Key)
			if !ok {
				continue
			}
			if s, ok := newTierSwap(b.Key, EffectivePromptText(b.Key, b.Text), o.Text, renders[b.Key]); ok {
				built[t] = append(built[t], s)
			}
		}
	}
	tierMu.Lock()
	if tierGen == gen {
		tierSwaps, tierBuiltAt, tierBuiltN = built, gen, len(blocks)
	}
	tierMu.Unlock()
	return built[tier]
}

// ApplyTierText rewrites a system prompt for the tier serving the call: each
// block that has its own text for that tier carries it in place of the shared
// text. tier is TierLead or TierWorker; anything else returns system as is.
func ApplyTierText(tier, system string) string {
	if system == "" || !validTier(tier) {
		return system
	}
	swaps := swapsFor(tier)
	if len(swaps) == 0 {
		return system
	}
	var hit []string
	for _, s := range swaps {
		if out, ok := s.apply(system); ok {
			system = out
			hit = append(hit, s.key)
		}
	}
	if len(hit) > 0 {
		now := time.Now()
		tierMu.Lock()
		for _, k := range hit {
			tierApplied[tier+"|"+k] = now
		}
		tierMu.Unlock()
	}
	return system
}

// TierTextApplied is when a tier's own text for a block last went out in a
// prompt that tier answered, since this process started; zero when it has
// not. A tier text that is never applied is one whose block is not in any
// prompt that tier is sent, or no longer matches the shared text it replaces.
func TierTextApplied(tier, key string) time.Time {
	tierMu.Lock()
	defer tierMu.Unlock()
	return tierApplied[tier+"|"+key]
}
