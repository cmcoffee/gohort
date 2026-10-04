package prompts

// The peer's wording: block text from the machine that serves this one's
// worker model.
//
// A model is tuned where it runs. The machine that lends its model to peers
// is the one whose tuning harness watches that model answer, so the overrides
// it writes are fitted to that model. A machine borrowing it reads them and
// puts them ahead of its own: when the worker is a peer's model, the peer's
// wording governs.
//
// Only registered blocks travel. The Style and global rules are stored as
// overrides too, but they are this operator's choice about how its agents
// write and what they may do, so they are never served to a peer and never
// taken from one, whatever key a peer sends.
//
// The layer is kept in the store as well as in memory, so a restart while the
// peer is down still has the last wording it sent instead of falling back to
// text that was fitted to some other model.

import (
	"strings"
	"sync"
	"time"
)

// peerLayerKey holds the whole layer as one record, apart from the
// prompt_override. keys so nothing that lists or clears local overrides can
// touch it.
const peerLayerKey = "prompt_peer_layer"

// peerLayer is the wording one peer sent, as kept.
type peerLayer struct {
	Source string            `json:"source"`
	Model  string            `json:"model,omitempty"`
	At     time.Time         `json:"at"`
	Blocks map[string]string `json:"blocks,omitempty"`
}

var (
	peerLayerMu  sync.RWMutex
	peerLayerNow peerLayer // Source is empty when no peer governs
)

// PeerLayerStatus says whose wording governs and how much of it, for display.
// Source is empty when no peer's wording is in force.
type PeerLayerStatus struct {
	Source string    // the peer's name
	Model  string    // the model the peer said it serves
	At     time.Time // when the wording was last fetched
	Count  int       // blocks the peer's wording replaces
}

// PeerLayerResult says what SetPeerPromptLayer kept and what it left out.
type PeerLayerResult struct {
	Kept int
	// Unknown are keys this machine has no registered block for: a block the
	// peer's build has and this one does not, or one this operator owns.
	Unknown []string
	// Broken are keys whose text names a placeholder the block cannot fill,
	// so taking it would put a literal {name} in front of the model.
	Broken []string
	// Changed is true when the wording in force is now different.
	Changed bool
}

// peerOverride is the peer's text for a block, when its wording has one.
func peerOverride(key string) (string, bool) {
	peerLayerMu.RLock()
	defer peerLayerMu.RUnlock()
	s, ok := peerLayerNow.Blocks[key]
	return s, ok && s != ""
}

// PeerPromptLayer reports the peer wording in force.
func PeerPromptLayer() PeerLayerStatus {
	peerLayerMu.RLock()
	defer peerLayerMu.RUnlock()
	l := peerLayerNow
	return PeerLayerStatus{Source: l.Source, Model: l.Model, At: l.At, Count: len(l.Blocks)}
}

// SetPeerPromptLayer puts a peer's wording in force. blocks is what the peer
// sent; only keys this machine has registered are kept, and a text that names
// a placeholder the shipped block does not have is left out, since nothing
// here would fill it. An empty blocks still records the peer as the source,
// which is the honest state for a peer that has no wording of its own.
func SetPeerPromptLayer(source, model string, at time.Time, blocks map[string]string) PeerLayerResult {
	var res PeerLayerResult
	shipped := map[string]string{}
	for _, b := range AllPromptBlocks() {
		shipped[b.Key] = b.Text
	}
	owned := operatorOwnedKeys()
	kept := map[string]string{}
	for key, text := range blocks {
		if strings.TrimSpace(text) == "" {
			continue
		}
		def, ok := shipped[key]
		if !ok || owned[key] {
			res.Unknown = append(res.Unknown, key)
			continue
		}
		if TierPlaceholderProblem(def, text) != "" {
			res.Broken = append(res.Broken, key)
			continue
		}
		kept[key] = text
	}
	res.Kept = len(kept)
	next := peerLayer{Source: strings.TrimSpace(source), Model: strings.TrimSpace(model), At: at, Blocks: kept}

	peerLayerMu.Lock()
	prev := peerLayerNow
	peerLayerNow = next
	peerLayerMu.Unlock()
	res.Changed = prev.Source != next.Source || prev.Model != next.Model || !sameBlocks(prev.Blocks, next.Blocks)

	// Saved every time, not only on a change, so the fetched time survives a
	// restart and the status can say how old the wording is.
	if db := promptOverrideStore(); db != nil {
		db.Set(OverrideTable, peerLayerKey, next)
	}
	if res.Changed {
		tierTextChanged()
	}
	return res
}

// ClearPeerPromptLayer drops the peer's wording, so the local overrides
// govern again. Reports whether there was any to drop.
func ClearPeerPromptLayer() bool {
	peerLayerMu.Lock()
	had := peerLayerNow.Source != "" || len(peerLayerNow.Blocks) > 0
	peerLayerNow = peerLayer{}
	peerLayerMu.Unlock()
	if db := promptOverrideStore(); db != nil {
		db.Unset(OverrideTable, peerLayerKey)
	}
	if had {
		tierTextChanged()
	}
	return had
}

// loadPeerLayer reads the last wording kept, or empties the layer when there
// is no store. Not filtered against the registry here: tool blocks register
// as tools are first seen, after this runs, and the next fetch filters again.
func loadPeerLayer(db Store) {
	var l peerLayer
	if db != nil {
		db.Get(OverrideTable, peerLayerKey, &l)
	}
	peerLayerMu.Lock()
	peerLayerNow = l
	peerLayerMu.Unlock()
}

// SharedBlockOverrides is the wording this machine serves to peers that
// borrow its model: the local override of every registered block that has
// one. The peer layer is not included, because what a peer reads should be
// what was tuned here, and the operator's own rules never leave the machine.
func SharedBlockOverrides() map[string]string {
	owned := operatorOwnedKeys()
	out := map[string]string{}
	for _, b := range AllPromptBlocks() {
		if owned[b.Key] {
			continue
		}
		if s, ok := localPromptOverride(b.Key); ok {
			out[b.Key] = s
		}
	}
	return out
}

// operatorOwnedKeys are the keys of the Style rules, the global rules and the
// operator's own blocks. Their overrides live beside the block overrides, but
// they belong to this deployment and never cross to or from a peer.
func operatorOwnedKeys() map[string]bool {
	owned := map[string]bool{}
	for _, r := range AllStyleRules() {
		owned[r.Key] = true
	}
	for _, r := range GlobalRules() {
		owned[r.Key] = true
	}
	for _, b := range CustomPromptBlocks() {
		owned[b.Key] = true
	}
	return owned
}

func sameBlocks(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
