// Watching the cached prefix for the thing that quietly re-writes it.
//
// Prompt caching bills three ways: fresh input at full price, a cache
// READ at a tenth, and a cache WRITE at a premium. A long tool-using
// turn should therefore be mostly reads — the tools and system prefix
// are written once and read on every subsequent call, and only the new
// tool results are written.
//
// When that goes wrong it does not look like an error. It looks like a
// bill. One reported turn read 5.06M tokens (healthy, ~24 calls over a
// ~210k prefix) and WROTE 1.39M — 58k per call, when the delta should
// have been a fraction of that. Writes were 77% of the cost.
//
// The prefix is ordered tools → system → messages, with a cache
// breakpoint after the tools and after the system block, so ANY change
// to the tool catalog or the system prompt between two calls of one turn
// invalidates everything after it and re-writes it. Several things can
// do that legitimately-looking: a machine phase narrowing the catalog, a
// lazily-loaded tool schema arriving mid-turn, a compaction pass
// rewriting earlier messages.
//
// This does not fix any of that. It NAMES it: which of the two changed,
// at which byte, and how much followed that byte and therefore had to be
// paid for again. A hypothesis about cache invalidation is unfalsifiable
// without that, and the alternative is guessing at three suspects.
//
// Off unless a caller labels the turn (WithPromptTurn), so nothing here
// costs anything for the callers this was not written for.
//
// Across conversations too. A label names a family (an agent) as well as the
// turn, and the first call of a conversation is compared with the family's
// last call: on a local server a new conversation reuses the cache only when
// its system prompt and tools match an earlier one byte for byte, so a slow
// first token is either a prefix that moved (named here) or a cache that let
// it go (said here as "same as its last conversation").

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

type promptTurnKey struct{}

// promptLabel is what WithPromptTurn puts on a context.
type promptLabel struct {
	family, turn string
}

// WithPromptTurn labels every LLM call made under ctx as belonging to
// one turn, which is the unit the watch compares within, and to a family
// (an agent), whose conversations are compared with each other. Without a
// turn the watch does nothing; without a family it compares within turns
// only.
func WithPromptTurn(ctx context.Context, family, turn string) context.Context {
	if turn = strings.TrimSpace(turn); turn == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, promptTurnKey{}, promptLabel{strings.TrimSpace(family), turn})
}

func promptTurn(ctx context.Context) promptLabel {
	if ctx == nil {
		return promptLabel{}
	}
	if l, ok := ctx.Value(promptTurnKey{}).(promptLabel); ok {
		return l
	}
	return promptLabel{}
}

// prefixSnapshot is what one call sent, kept only long enough to compare
// the next call against it.
type prefixSnapshot struct {
	calls     int
	system    string
	toolNames []string
	toolSums  map[string]string // name -> hash of what the model reads of it
}

var (
	prefixWatchMu sync.Mutex
	prefixWatch   = map[string]*prefixSnapshot{}
	// prefixFamilies is the first call of each family's latest conversation,
	// for the next one. The first call, not the last: a tool loaded partway
	// through a conversation belongs to it alone, and comparing the next
	// conversation with that reported a catalog change that was not one.
	prefixFamilies = map[string]*prefixSnapshot{}
)

// maxWatchedTurns bounds the map. A turn ends without telling anyone, so
// entries are evicted by pressure rather than by lifetime — the map is a
// diagnostic, not a ledger, and losing the oldest costs one comparison.
const maxWatchedTurns = 64

// WatchPromptPrefix compares this call's cached prefix against the
// previous call of the same turn and logs what moved.
//
// Called at the shared option-application point, so it sees what every
// provider is about to send rather than one provider's rendering of it.
func WatchPromptPrefix(ctx context.Context, system string, tools []Tool) {
	label := promptTurn(ctx)
	turn := label.turn
	if turn == "" {
		return
	}
	names := make([]string, 0, len(tools))
	sums := make(map[string]string, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
		sums[t.Name] = toolSum(t)
	}

	prefixWatchMu.Lock()
	defer prefixWatchMu.Unlock()
	prev, seen := prefixWatch[turn]
	if !seen {
		if len(prefixWatch) >= maxWatchedTurns {
			for k := range prefixWatch {
				delete(prefixWatch, k)
				break
			}
		}
		prefixWatch[turn] = &prefixSnapshot{calls: 1, system: system, toolNames: names, toolSums: sums}
		Debug("[prefix-watch] %s call 1: %d tools, %d bytes of system prompt, this is the write everything else should read",
			turn, len(names), len(system))
		if label.family == "" {
			return
		}
		if last, ok := prefixFamilies[label.family]; ok {
			Log("[prefix-watch] %s: a new conversation, against its family's last one: %s", turn, prefixAgainst(last, system, names, sums))
		} else if len(prefixFamilies) >= maxWatchedTurns {
			prefixFamilies = map[string]*prefixSnapshot{}
		}
		prefixFamilies[label.family] = &prefixSnapshot{system: system, toolNames: names, toolSums: sums}
		return
	}
	prev.calls++

	if added, removed := diffNames(prev.toolNames, names); len(added) > 0 || len(removed) > 0 {
		// Tools render FIRST, so a change here re-writes the system
		// prompt and the whole conversation behind it. This is the
		// expensive one.
		Log("[prefix-watch] %s call %d: TOOL CATALOG CHANGED (%d → %d%s%s), tools sit before everything, so the system prompt and the entire conversation are re-written this call",
			turn, prev.calls, len(prev.toolNames), len(names),
			listPart(" +", added), listPart(" -", removed))
		prev.toolNames = names
	}

	if prev.system != system {
		at := firstDiff(prev.system, system)
		Log("[prefix-watch] %s call %d: SYSTEM PROMPT CHANGED at byte %d of %d, %d bytes after it are re-written this call. Was %q, now %q",
			turn, prev.calls, at, len(system), maxInt(len(system)-at, 0),
			snippet(prev.system, at), snippet(system, at))
		prev.system = system
	}
}

// prefixAgainst says how a prefix compares with an earlier one: the same, or
// what moved first.
func prefixAgainst(last *prefixSnapshot, system string, names []string, sums map[string]string) string {
	var moved []string
	if added, removed := diffNames(last.toolNames, names); len(added) > 0 || len(removed) > 0 {
		moved = append(moved, "TOOLS CHANGED ("+itoa(len(last.toolNames))+" → "+itoa(len(names))+listPart(" +", added)+listPart(" -", removed)+")")
	} else if strings.Join(last.toolNames, ",") != strings.Join(names, ",") {
		moved = append(moved, "TOOLS REORDERED")
	}
	var reworded []string
	for _, n := range names {
		if was, ok := last.toolSums[n]; ok && was != sums[n] {
			reworded = append(reworded, n)
		}
	}
	if len(reworded) > 0 {
		moved = append(moved, "TOOL SCHEMAS CHANGED"+listPart(" ", reworded))
	}
	if last.system != system {
		at := firstDiff(last.system, system)
		moved = append(moved, "SYSTEM PROMPT CHANGED at byte "+itoa(at)+" of "+itoa(len(system))+": was "+quote(snippet(last.system, at))+", now "+quote(snippet(system, at)))
	}
	if len(moved) == 0 {
		return "same system prompt and tools, so a cold start here is the server's cache letting the prefix go, not the prompt"
	}
	return strings.Join(moved, "; ")
}

// toolSum identifies what the model reads of a tool, so a reworded
// description shows up and not only an added or removed tool.
func toolSum(t Tool) string {
	b, _ := json.Marshal(struct {
		D string
		P map[string]ToolParam
		R []string
	}{t.Description, t.Parameters, t.Required})
	return hashOf(string(b))
}

func quote(s string) string { return strconv.Quote(s) }

// diffNames reports catalog membership changes. Order is deliberately
// ignored: a reordered catalog also breaks the cache, but reporting it as
// "everything changed" buries the case somebody can act on.
func diffNames(before, after []string) (added, removed []string) {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	has := make(map[string]bool, len(after))
	for _, n := range after {
		has[n] = true
		if !had[n] {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !has[n] {
			removed = append(removed, n)
		}
	}
	return added, removed
}

func listPart(label string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) > 6 {
		names = append(names[:6:6], "...")
	}
	return label + strings.Join(names, ",")
}

// firstDiff returns the byte offset where two strings diverge. Bytes,
// not runes: the cache keys on bytes and so should the number a person
// uses to find the culprit.
func firstDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// snippet shows a little of what sits at the divergence, which is
// usually enough to recognize the block that moved.
func snippet(s string, at int) string {
	if at > len(s) {
		at = len(s)
	}
	end := at + 60
	if end > len(s) {
		end = len(s)
	}
	out := strings.ReplaceAll(s[at:end], "\n", "⏎")
	if end < len(s) {
		out += "..."
	}
	return out
}

// hashOf is used by tests and by callers that want a stable identity for
// a prefix without holding the whole string.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
