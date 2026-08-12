// Resource-sharing keys — how one gohort instance lends its infrastructure to
// another.
//
// The problem this solves: a deployment with a GPU, a local model server and an
// embedder has capacity that a second deployment (a laptop, a Mac, a small VPS)
// has no way to reach. Both instances already know how to TALK to a model
// server; what is missing is a way for one to BE one, safely, for a peer it
// chooses.
//
// A PeerKey is deliberately NOT a user credential, and that distinction is the
// whole security design.
//
// The obvious implementation is to hand a peer key to RegisterAPIKeyValidator
// alongside the desktop key. Do not. A validator there returns a USERNAME, and
// userFromAPIKey makes the bearer that user everywhere a request is
// authenticated — every app, every tool, every stored credential they own. A
// key meaning "you may use my GPU" would silently also mean "you may read my
// mail". So a peer key resolves to a PeerKey and nothing else, it is checked
// only by handlers that ask for it by name, and those handlers never call
// AuthCurrentUser or userFromAPIKey. A peer is a principal with capabilities,
// not a person.
//
// Capabilities are an allowlist per key, so "the Mac may embed" does not become
// "the Mac may render" the day image sharing ships.
package core

import (
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// peerKeysTable holds PeerKey records in RootDB, keyed by ID.
const peerKeysTable = "peer_keys"

// Capability names a PeerKey can grant. Each is a distinct resource this
// instance is willing to spend on someone else's behalf.
//
// Embeddings, images and transcription are served today. The rest are declared
// so the manifest can report them as unavailable rather than omitting them — a
// peer asking "what do you offer" should learn the vocabulary, not have to
// guess at it.
//
// Transcribe is speech-to-text and is NOT transcode: one turns audio into
// words, the other turns a media file into a different container. They read
// alike and share four letters, which is reason enough to say so here rather
// than let someone wire a whisper endpoint to the transcode grant.
const (
	PeerCapEmbeddings = "embeddings"
	PeerCapImages     = "images"
	PeerCapTranscribe = "transcribe"
	PeerCapSearch     = "search"
	PeerCapBrowse     = "browse"
	PeerCapModels     = "models"
	PeerCapTranscode  = "transcode"
	// PeerCapInvestigate lets a peer ask THIS instance to investigate one of a
	// named user's appliances and return the answer.
	//
	// Unlike every other capability, this one is not anonymous compute. The
	// other six take an input and give back a derived output; this one reaches a
	// specific user's systems, so a key granting it must also name WHOSE
	// appliances and WHICH ones (Owner + Appliances below). A capability grant
	// alone is not enough to reach anything.
	PeerCapInvestigate = "investigate"
	// PeerCapKnowledge lets a peer pull the knowledge already GATHERED about
	// those same appliances — the structured docs and facts — so it can answer
	// from them locally instead of paying a live investigation for something
	// written down months ago.
	//
	// Deliberately separate from investigate. They are different postures: one
	// lets a peer ask a question and receive prose, the other lets it hold a
	// COPY of what this instance knows. An operator may reasonably want either
	// without the other, and folding them together would make that unsayable.
	PeerCapKnowledge = "knowledge"
	// PeerCapExec lets a peer run commands on those appliances through this
	// instance's connection to them — the peer as a WIRE to a machine it cannot
	// route to itself.
	//
	// This is the widest grant here and the description should not soften it: a
	// key granted exec has a shell on the named appliances, gated by the CALLING
	// instance rather than this one. It is for instances the same operator owns
	// on both ends, and it is per appliance and revocable for that reason.
	PeerCapExec = "exec"
)

// PeerCapabilities is every capability name, in a stable order.
func PeerCapabilities() []string {
	return []string{PeerCapEmbeddings, PeerCapImages, PeerCapTranscribe, PeerCapSearch, PeerCapBrowse, PeerCapModels, PeerCapTranscode, PeerCapInvestigate, PeerCapKnowledge, PeerCapExec}
}

// peerCapServed reports whether this build can actually serve a capability.
// A key may GRANT a capability that is not implemented yet; the manifest says
// so plainly rather than advertising something that would 404.
func peerCapServed(cap string) bool {
	switch cap {
	case PeerCapEmbeddings, PeerCapImages, PeerCapTranscribe, PeerCapSearch, PeerCapBrowse, PeerCapInvestigate, PeerCapKnowledge, PeerCapExec:
		return true
	}
	return false
}

// PeerCapabilityServed is peerCapServed for callers outside core — the admin UI
// builds its capability checklist from it, so a capability that ships stops
// being labelled "not implemented" without anyone remembering to edit the copy.
func PeerCapabilityServed(cap string) bool { return peerCapServed(cap) }

// PeerKey is one remote instance's grant. The secret is stored as issued: it
// authenticates a machine the operator explicitly invited, and it must be
// re-displayable in the admin UI for a re-paste into the peer's config, which
// a hash would prevent.
type PeerKey struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`                // the secret the peer presents
	Label    string   `json:"label"`              // who this was issued to, for the operator
	Caps     []string `json:"caps"`               // allowlist; empty grants nothing
	RatePerM int      `json:"rate_per_min"`       // calls/minute ceiling, 0 = default
	Disabled bool     `json:"disabled,omitempty"` // revoked without deleting the record
	Created  string   `json:"created"`
	LastSeen string   `json:"last_seen,omitempty"`
	Calls    int64    `json:"calls,omitempty"` // lifetime, for the admin table
	// Owner is the LOCAL user whose appliances an investigate grant reaches.
	// Required for PeerCapInvestigate and meaningless for the others: an
	// embedding request is anonymous compute, an investigation is somebody's
	// machines. The investigation runs AS this user, so it can reach exactly
	// what they can and nothing else.
	Owner string `json:"owner,omitempty"`
	// Appliances is the explicit set of appliance ids this key may investigate.
	// EMPTY GRANTS NOTHING, and there is deliberately no wildcard — the same
	// rule command_grants.go applies to agents, for the same reason: "every
	// appliance, including the ones that do not exist yet" is a decision nobody
	// can review, and a list of ids can be read.
	Appliances []string `json:"appliances,omitempty"`
}

// AllowsAppliance reports whether this key may investigate one appliance.
func (k PeerKey) AllowsAppliance(id string) bool {
	return k.AllowsApplianceFor(PeerCapInvestigate, id)
}

// AllowsApplianceFor reports whether this key may use one appliance-scoped
// capability against one appliance. Three things must hold: the capability, an
// owner to run as, and this specific id.
//
// The appliance LIST is shared between investigate and knowledge — it answers
// "which systems", and the capabilities answer "what may be done with them".
// Two lists would let them drift, and a peer granted knowledge of a system it
// cannot ask about (or the reverse) is a distinction nobody wants to maintain
// per id.
func (k PeerKey) AllowsApplianceFor(capability, id string) bool {
	if !k.Allows(capability) || strings.TrimSpace(k.Owner) == "" {
		return false
	}
	for _, a := range k.Appliances {
		if a == id {
			return true
		}
	}
	return false
}

// defaultPeerRatePerMin caps an unconfigured key. A peer sharing an embedder is
// expected to make steady small calls; a runaway loop on the other end should
// hit a ceiling here rather than saturating this instance's GPU. Generous
// enough for bulk ingestion, low enough to be a real limit.
const defaultPeerRatePerMin = 600

// Allows reports whether this key grants a capability. A disabled key allows
// nothing, so revocation needs no separate check at every call site.
func (k PeerKey) Allows(cap string) bool {
	if k.Disabled {
		return false
	}
	for _, c := range k.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

// rate returns the per-minute ceiling in force for this key.
func (k PeerKey) rate() int {
	if k.RatePerM > 0 {
		return k.RatePerM
	}
	return defaultPeerRatePerMin
}

// MintPeerKey issues a new key with the given label and capability allowlist.
// Unknown capability names are rejected rather than silently stored: a typo
// ("embedding") would otherwise produce a key that authenticates fine and is
// refused by every handler, which reads as a broken key.
func MintPeerKey(label string, caps []string, ratePerMin int) (PeerKey, error) {
	if RootDB == nil {
		return PeerKey{}, fmt.Errorf("no database available")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return PeerKey{}, fmt.Errorf("a label is required — it is how you will recognize this peer later")
	}
	clean, err := cleanPeerCaps(caps)
	if err != nil {
		return PeerKey{}, err
	}
	pk := PeerKey{
		ID:       UUIDv4(),
		Key:      strings.ReplaceAll(UUIDv4()+UUIDv4(), "-", ""),
		Label:    label,
		Caps:     clean,
		RatePerM: ratePerMin,
		Created:  time.Now().Format(time.RFC3339),
	}
	RootDB.Set(peerKeysTable, pk.ID, pk)
	Log("[peer] minted key for %q granting %s", pk.Label, strings.Join(pk.Caps, ", "))
	return pk, nil
}

// cleanPeerCaps normalizes and validates a capability grant: lowercased,
// de-duplicated, sorted, every name known, at least one present.
//
// Shared by minting and re-granting so the two can never diverge — a capability
// that mint would refuse must not be settable through an edit.
func cleanPeerCaps(caps []string) ([]string, error) {
	known := map[string]bool{}
	for _, c := range PeerCapabilities() {
		known[c] = true
	}
	var clean []string
	seen := map[string]bool{}
	for _, c := range caps {
		c = strings.TrimSpace(strings.ToLower(c))
		if c == "" || seen[c] {
			continue
		}
		if !known[c] {
			return nil, fmt.Errorf("unknown capability %q (known: %s)", c, strings.Join(PeerCapabilities(), ", "))
		}
		seen[c] = true
		clean = append(clean, c)
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("grant at least one capability (%s) — a key with none can authenticate but do nothing", strings.Join(PeerCapabilities(), ", "))
	}
	sort.Strings(clean)
	return clean, nil
}

// SetPeerKeyCaps re-grants an EXISTING key, keeping its secret.
//
// Without this the only way to widen or narrow a grant was to delete the key
// and mint a new one — which changes the secret, so every peer holding it has
// to be re-pasted and re-probed. That turned "also let them transcribe" into a
// two-machine operation, and made shipping a new capability something no
// existing peer could pick up without an outage.
//
// Narrowing takes effect immediately and needs no separate revocation: Allows
// reads this list on every call.
func SetPeerKeyCaps(id string, caps []string) (PeerKey, error) {
	if RootDB == nil || strings.TrimSpace(id) == "" {
		return PeerKey{}, fmt.Errorf("no such key")
	}
	clean, err := cleanPeerCaps(caps)
	if err != nil {
		return PeerKey{}, err
	}
	var pk PeerKey
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return PeerKey{}, fmt.Errorf("no such key")
	}
	was := strings.Join(pk.Caps, ", ")
	pk.Caps = clean
	RootDB.Set(peerKeysTable, id, pk)
	Log("[peer] key %q re-granted: %s -> %s", pk.Label, was, strings.Join(clean, ", "))
	return pk, nil
}

// ListPeerKeys returns every issued key, newest first.
func ListPeerKeys() []PeerKey {
	if RootDB == nil {
		return nil
	}
	var out []PeerKey
	for _, id := range RootDB.Keys(peerKeysTable) {
		var pk PeerKey
		if RootDB.Get(peerKeysTable, id, &pk) {
			out = append(out, pk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// LookupPeerKey resolves a presented secret to its key record.
//
// The comparison is constant-time and every candidate is checked: returning
// early on the first mismatch leaks, through timing, how much of a guessed key
// was right. The scan is over the operator's own small list of invited peers,
// so the cost is irrelevant next to the property.
func LookupPeerKey(secret string) (PeerKey, bool) {
	secret = strings.TrimSpace(secret)
	if RootDB == nil || secret == "" {
		return PeerKey{}, false
	}
	var found PeerKey
	var ok bool
	for _, id := range RootDB.Keys(peerKeysTable) {
		var pk PeerKey
		if !RootDB.Get(peerKeysTable, id, &pk) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(pk.Key), []byte(secret)) == 1 {
			found, ok = pk, true
		}
	}
	if ok && found.Disabled {
		return found, false
	}
	return found, ok
}

// SetPeerKeyDisabled revokes (or restores) a key without discarding its record,
// so the operator keeps the audit trail of what was issued to whom.
func SetPeerKeyDisabled(id string, disabled bool) bool {
	if RootDB == nil || id == "" {
		return false
	}
	var pk PeerKey
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return false
	}
	pk.Disabled = disabled
	RootDB.Set(peerKeysTable, id, pk)
	Log("[peer] key %q disabled=%v", pk.Label, disabled)
	return true
}

// DeletePeerKey removes a key outright.
func DeletePeerKey(id string) bool {
	if RootDB == nil || id == "" {
		return false
	}
	var pk PeerKey
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return false
	}
	RootDB.Unset(peerKeysTable, id)
	Log("[peer] deleted key %q", pk.Label)
	return true
}

// --- rate limiting -----------------------------------------------------------

// peerRate tracks calls per key within a rolling one-minute window. In memory
// on purpose: the ceiling exists to stop a runaway peer from saturating this
// instance, and a limiter that survives restarts is not needed for that.
var (
	peerRateMu sync.Mutex
	peerRate   = map[string]*peerWindow{}
)

type peerWindow struct {
	start time.Time
	n     int
}

// peerRateAllow records one call against a key and reports whether it fits
// under the ceiling.
func peerRateAllow(k PeerKey) bool {
	peerRateMu.Lock()
	defer peerRateMu.Unlock()
	w := peerRate[k.ID]
	now := time.Now()
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &peerWindow{start: now}
		peerRate[k.ID] = w
	}
	if w.n >= k.rate() {
		return false
	}
	w.n++
	return true
}

// touchPeerKey records that a key was used. Best-effort and deliberately not on
// the hot path's critical section — the counter is for the admin table, and a
// lost increment matters less than a slower request.
func touchPeerKey(k PeerKey) {
	if RootDB == nil {
		return
	}
	var cur PeerKey
	if !RootDB.Get(peerKeysTable, k.ID, &cur) {
		return
	}
	cur.LastSeen = time.Now().Format(time.RFC3339)
	cur.Calls++
	RootDB.Set(peerKeysTable, cur.ID, cur)
}

// SetPeerKeyScope sets WHOSE appliances an investigate grant reaches and WHICH
// ones. Separate from SetPeerKeyCaps because the two answer different questions
// and are edited at different moments: the capability says what kind of work a
// peer may ask for, the scope says what it may ask about.
//
// Both are replaced wholesale, so clearing the list revokes the reach without
// touching the capability — the way to say "still an investigate peer, just not
// for anything right now" rather than having to re-grant later.
//
// An owner is required whenever appliances are named: an investigation runs AS
// a user, and a list with nobody to run it as would authorize a request that
// then fails at dispatch, which is a worse failure than refusing here.
func SetPeerKeyScope(id, owner string, appliances []string) (PeerKey, error) {
	if RootDB == nil || strings.TrimSpace(id) == "" {
		return PeerKey{}, fmt.Errorf("no such key")
	}
	var pk PeerKey
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return PeerKey{}, fmt.Errorf("no such key")
	}
	owner = strings.TrimSpace(owner)
	clean := make([]string, 0, len(appliances))
	seen := map[string]bool{}
	for _, a := range appliances {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		clean = append(clean, a)
	}
	if len(clean) > 0 && owner == "" {
		return PeerKey{}, fmt.Errorf("naming appliances needs an owner — an investigation runs as a user")
	}
	// Every id must actually resolve for that owner. Without this a grant can
	// name an appliance the owner cannot see — wrong user, never shared, since
	// deleted — and it looks correct in the admin table right up until a
	// question arrives from a machine nobody is sitting at and fails with
	// "appliance not found". Refuse where the operator can still fix it.
	//
	// It doubles as the authorization rule for granting from the appliance
	// side: you can only add a box to a key whose owner could already reach it,
	// so the grant can never widen what that identity sees.
	if len(clean) > 0 && PeerInvestigableFunc != nil {
		reachable := map[string]bool{}
		for _, it := range PeerInvestigableFunc(owner, clean) {
			reachable[it.ID] = true
		}
		var bad []string
		for _, id := range clean {
			if !reachable[id] {
				bad = append(bad, id)
			}
		}
		if len(bad) > 0 {
			return PeerKey{}, fmt.Errorf(
				"%s cannot reach %s — an investigation runs as that user, so the grant would fail when used",
				owner, strings.Join(bad, ", "))
		}
	}
	pk.Owner, pk.Appliances = owner, clean
	RootDB.Set(peerKeysTable, id, pk)
	Log("[peer] key %q scope set: owner=%q appliances=%d", pk.Label, owner, len(clean))
	return pk, nil
}
