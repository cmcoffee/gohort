// The consuming half of resource sharing: another instance registered HERE as
// a provider of capabilities this one can use.
//
// The serving side (peer_key.go / peer_serve.go) issues a key. This side takes
// that key plus an address, asks the remote what it offers, and remembers the
// answer so the rest of the admin UI can offer it as a choice — "use this
// peer's embedder" as a dropdown, rather than an operator hand-copying a URL,
// a model name and a token into three fields and hoping they match.
//
// Resolution happens at SAVE time, not at call time: picking a peer writes the
// ordinary endpoint/model/key into EmbeddingConfig. Everything downstream —
// Embed, EmbedVersion, the vector store — keeps working with no knowledge that
// a peer exists. A capability that had to teach every consumer about peers
// would be a much larger change and a much easier one to get wrong.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// remotePeersTable holds RemotePeer records in RootDB, keyed by Name.
const remotePeersTable = "remote_peers"

// remotePeerNameRE keeps a peer's local nickname simple enough to appear in a
// select value ("peer:<name>") without quoting.
var remotePeerNameRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// RemotePeer is another instance this one may borrow capabilities from.
//
// Caps is what the remote actually SERVES and this key was GRANTED — the
// intersection, computed at probe time. Storing the intersection rather than
// the raw grant means the UI offers only choices that will work.
type RemotePeer struct {
	Name       string   `json:"name"`     // local nickname
	BaseURL    string   `json:"base_url"` // https://host — no /api/peer suffix
	Key        string   `json:"key"`      // the peer key that instance issued
	Caps       []string `json:"caps"`     // served AND granted, from the manifest
	Instance   string   `json:"instance,omitempty"`
	EmbedModel string   `json:"embed_model,omitempty"`
	EmbedDim   int      `json:"embed_dim,omitempty"`
	// TranscribeModel is the speech model the peer said it uses. Advisory —
	// unlike EmbedModel, a mismatch produces worse text rather than vectors in
	// the wrong space — but stored so the operator can see what they picked.
	TranscribeModel string `json:"transcribe_model,omitempty"`
	// SearchProvider is the upstream the peer searches with ("serper",
	// "duckduckgo"). Shown to the operator because borrowing a free DuckDuckGo
	// relay and borrowing a metered Serper key look identical otherwise.
	SearchProvider string `json:"search_provider,omitempty"`
	LastChecked    string `json:"last_checked,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	// ImageConnectors names the local rest_image connectors provisioned from
	// this peer's advertised renderers. Stored rather than recomputed: teardown
	// reads this list, so a connector the operator made themselves can never be
	// swept up by a peer being forgotten.
	ImageConnectors []string `json:"image_connectors,omitempty"`
	// Investigable is what this peer's manifest said THIS instance's key may
	// ask about. Cached on the record so the "add a remote system" picker can
	// offer real names without a round trip, and refreshed whenever the
	// manifest is re-read.
	Investigable []PeerInvestigable `json:"investigable,omitempty"`
	// UseTokens opts this peer into the rotating-credential flow (see
	// peer_token_client.go). Off by default and off for every peer added before
	// it existed, because the static key in Key keeps working either way and
	// switching a live link over is the operator's call, not a release's.
	//
	// Turned on automatically when the peer's manifest says exchange is
	// REQUIRED, since at that point the static key is refused and there is
	// nothing to decide.
	UseTokens bool `json:"use_tokens,omitempty"`
	// AccessToken, AccessExpires and RefreshToken are the current lease.
	//
	// Persisted so a restart resumes on the credential it already holds rather
	// than exchanging again — every needless exchange is another token family
	// on the serving side, and on a rotating grant the pairing code that would
	// buy one is single use.
	//
	// Key is NOT replaced by these. It stays as the fallback and as the pairing
	// code, which is what makes the whole flow reversible.
	AccessToken   string `json:"access_token,omitempty"`
	AccessExpires string `json:"access_expires,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
}

// Offers reports whether this peer can serve a capability.
func (p RemotePeer) Offers(cap string) bool {
	for _, c := range p.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

// EmbeddingsURL is the base an OpenAI-shaped embedding client points at.
func (p RemotePeer) EmbeddingsURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1"
}

// NormalizePeerBaseURL trims what an operator is likely to paste. The admin UI
// on the serving side shows paths like /api/peer/manifest and /api/peer/v1, so
// those get copied along with the host more often than not; silently accepting
// them beats an error about a URL that looks right.
func NormalizePeerBaseURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimRight(u, "/")
	for _, suffix := range []string{"/api/peer/manifest", "/api/peer/v1/embeddings", "/api/peer/v1", "/api/peer"} {
		if strings.HasSuffix(u, suffix) {
			u = strings.TrimSuffix(u, suffix)
			break
		}
	}
	return strings.TrimRight(u, "/")
}

// peerErrorBody reads the {"error": "..."} a peer endpoint answers with.
// Bounded, because this is an error path reading a body from another machine
// and a peer that answers a megabyte of HTML should cost one line in a log
// rather than a buffer.
func peerErrorBody(body io.Reader) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 8<<10)).Decode(&e); err != nil {
		return ""
	}
	return strings.TrimSpace(e.Error)
}

// ProbeRemotePeer asks an instance what it offers, without storing anything.
// Used both to validate a peer being added and to refresh an existing one.
func ProbeRemotePeer(ctx context.Context, baseURL, key string) (PeerManifest, error) {
	base := NormalizePeerBaseURL(baseURL)
	if base == "" {
		return PeerManifest{}, fmt.Errorf("an address is required")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return PeerManifest{}, fmt.Errorf("the address must start with http:// or https:// (got %q)", baseURL)
	}
	if strings.TrimSpace(key) == "" {
		return PeerManifest{}, fmt.Errorf("a peer key is required — mint one in the OTHER instance's Resource Sharing settings")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/peer/manifest", nil)
	if err != nil {
		return PeerManifest{}, err
	}
	req.Header.Set(peerKeyHeader, strings.TrimSpace(key))

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return PeerManifest{}, fmt.Errorf("could not reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		// The far side's own words, when it gave any. It is the only side that
		// can tell a spent pairing code from an unknown one, and replacing its
		// answer with a guess ("check it was not revoked") sent an operator
		// looking for a paste error in a string that was correct and used up.
		if why := peerErrorBody(resp.Body); why != "" {
			return PeerManifest{}, fmt.Errorf("%s refused that key: %s", base, why)
		}
		return PeerManifest{}, fmt.Errorf("%s did not recognize that key — check it was not revoked, and that it came from THAT instance", base)
	case http.StatusNotFound:
		return PeerManifest{}, fmt.Errorf("%s has no peer endpoint — it may be an older build, or the address may point at something else", base)
	default:
		return PeerManifest{}, fmt.Errorf("%s answered HTTP %d", base, resp.StatusCode)
	}
	var m PeerManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return PeerManifest{}, fmt.Errorf("could not read the manifest from %s: %w", base, err)
	}
	return m, nil
}

// adoptPeerTokenFlow decides whether this peer is on rotating credentials.
//
// Switched on automatically only when the far side says exchange is REQUIRED,
// because at that point the static key is refused and there is nothing left to
// decide. An operator who turned it on by hand keeps it on: the manifest saying
// "not required" means the static key would ALSO work, not that tokens have
// stopped working, and silently downgrading a link the operator deliberately
// hardened is not a decision a refresh should make.
//
// Never latches on for a peer that merely offers the endpoint. Doing so would
// have every existing pairing quietly start rotating on the next refresh, which
// is precisely the flag day the opt-in exists to avoid.
func adoptPeerTokenFlow(p RemotePeer, m PeerManifest) bool {
	if m.Token != nil && m.Token.Required {
		if !p.UseTokens {
			Log("[peer] %q now requires credential exchange — switching to rotating tokens", p.Name)
			// The refresh that fixes it clears the warning that named it, so
			// an operator who followed the instruction sees it stop rather
			// than having to infer that it worked.
			warnPeerResolveOnce("tokens:"+p.Name, "")
		}
		return true
	}
	return p.UseTokens
}

// usableCaps reduces a manifest to what is both served there and granted here.
func usableCaps(m PeerManifest) []string {
	var out []string
	for _, e := range m.Capabilities {
		if e.Served && e.Granted {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// refreshedCaps is usableCaps for a RE-probe: same rule, plus it keeps a
// capability that is still granted but was momentarily not being served.
//
// The two halves of "usable" have very different lifetimes. GRANTED is an
// operator's decision on the far side and changes when someone changes it;
// SERVED is a property of the far PROCESS, and is false for the window
// between that process accepting connections and the app that implements the
// capability finishing its wiring. A refresh landing inside that window used
// to write the capability out of the local record — and since a call checks
// the local record first (PeerExec, PeerInvestigate), the capability then
// failed HERE, without a request, until the next refresh half an hour later.
// The far side was fine the whole time.
//
// A withdrawal still takes effect immediately, because a withdrawal clears
// GRANTED. And nothing is invented: a capability is only retained if it was
// already usable, which it could only have become through a probe that saw
// it both served and granted.
func refreshedCaps(prev []string, m PeerManifest) []string {
	had := map[string]bool{}
	for _, c := range prev {
		had[c] = true
	}
	var out []string
	for _, e := range m.Capabilities {
		switch {
		case e.Served && e.Granted:
			out = append(out, e.Name)
		case e.Granted && had[e.Name]:
			// Still ours to use; that instance just is not answering for it
			// this second.
			out = append(out, e.Name)
			Debug("[peer] %s reports %q granted but not currently served — keeping it, it worked before", m.Instance, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// servesCap reports whether a manifest says a capability is being served
// right now, whatever this key is granted.
func servesCap(m PeerManifest, cap string) bool {
	for _, e := range m.Capabilities {
		if e.Name == cap {
			return e.Served
		}
	}
	return false
}

// SaveRemotePeer validates, probes and stores a peer. The probe is not
// optional: a peer saved without one would sit in the picker looking usable
// and fail at the first embed, at which point the cause is three screens away.
func SaveRemotePeer(ctx context.Context, name, baseURL, key string) (RemotePeer, error) {
	if RootDB == nil {
		return RemotePeer{}, fmt.Errorf("no database available")
	}
	name = strings.TrimSpace(strings.ToLower(name))
	if !remotePeerNameRE.MatchString(name) {
		return RemotePeer{}, fmt.Errorf("name must be lowercase letters, digits, - or _ (got %q)", name)
	}
	m, err := ProbeRemotePeer(ctx, baseURL, key)
	if err != nil {
		return RemotePeer{}, err
	}
	caps := usableCaps(m)
	if len(caps) == 0 {
		return RemotePeer{}, fmt.Errorf("%s reachable, but this key can use nothing there — "+
			"grant it a capability in that instance's Resource Sharing settings", NormalizePeerBaseURL(baseURL))
	}
	p := RemotePeer{
		Name:        name,
		BaseURL:     NormalizePeerBaseURL(baseURL),
		Key:         strings.TrimSpace(key),
		Caps:        caps,
		Instance:    m.Instance,
		LastChecked: time.Now().Format(time.RFC3339),
	}
	if m.Embeddings != nil {
		p.EmbedModel, p.EmbedDim = m.Embeddings.Model, m.Embeddings.Dim
	}
	if m.Transcribe != nil {
		p.TranscribeModel = m.Transcribe.Model
	}
	if m.Search != nil {
		p.SearchProvider = m.Search.Provider
	}
	p.UseTokens = adoptPeerTokenFlow(p, m)
	// Renderers become local backends immediately. A peer that announces it
	// offers rendering and then does not appear in the picker is indisputably
	// broken from the operator's side.
	syncPeerImages(&p, m.Images)
	RootDB.Set(remotePeersTable, name, p)
	// Anything resolving through this peer picks the new record up now rather
	// than at the end of the TTL — an operator who just pasted a rotated key
	// should not have to wonder whether it took.
	InvalidatePeerResolution()
	// Exchange AFTER the record exists: storing a credential writes onto it, so
	// pairing first would drop the tokens on the floor. Image connectors are
	// provisioned above against the static key and republished by the exchange,
	// which is the ordering that leaves no window holding a stale secret.
	if p.UseTokens {
		if terr := EnsurePeerToken(ctx, p); terr != nil {
			p.LastError = terr.Error()
			RootDB.Set(remotePeersTable, name, p)
			InvalidatePeerResolution()
			return p, fmt.Errorf("paired with %s but could not obtain a credential: %w", p.BaseURL, terr)
		}
		p, _ = GetRemotePeer(name)
	}
	Log("[peer] added remote %q at %s offering %s", name, p.BaseURL, strings.Join(caps, ", "))
	return p, nil
}

// UpdateRemotePeerKey re-pairs an EXISTING peer with a new pairing code,
// keeping its name and address.
//
// The recovery path for the other half of re-issue. A code is spent the moment
// its peer pairs, and re-issuing on the serving side kills the credentials this
// side is holding — so without a door to paste the new code into, the only way
// back was to forget the peer and add it again. That is not equivalent: forget
// drops the image connectors it contributed and any setting naming it, so an
// operator recovering from a rotation would take an outage in image generation
// to fix one in everything else.
//
// The old tokens are dropped BEFORE the probe. They are dead the instant the
// far side re-issued, and leaving them in place means a failed exchange falls
// back on a credential that cannot work — which reads as "the new key is wrong"
// when the new key is fine.
func UpdateRemotePeerKey(ctx context.Context, name, key string) (RemotePeer, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if strings.TrimSpace(key) == "" {
		return RemotePeer{}, fmt.Errorf("a key is required")
	}
	p, ok := GetRemotePeer(name)
	if !ok {
		return RemotePeer{}, fmt.Errorf("no peer named %q is registered", name)
	}
	// EXCLUSIVE for the whole re-key. Ordering alone was not enough: clearing
	// the credential makes every reader notice, and a background renewal that
	// starts in that window spends the operator's brand-new pairing code — it
	// is single use, so the probe below then finds it already exchanged and
	// reports that the far side did not recognize a key that was perfectly
	// good. Seen live. The lock is what makes "paste a new code" an operation
	// rather than a race, and the renewal gives up rather than joining in.
	release := lockPeerCredential(name)
	defer release()
	// THE NEW KEY GOES ON THE RECORD FIRST, and the order still matters — the
	// lock keeps our own renewals out, not a reader on another node or a
	// restart mid-way.
	//
	// Clearing the credential is what makes the re-pair take effect — the old
	// access token is dead the moment the far side re-issues, but it is not
	// EXPIRED, so EnsurePeerToken would look at its clock, decide the peer is
	// fine, and never exchange. But clearing it while the record still holds
	// the OLD key opens a window: every reader that calls PeerCredential in it
	// finds nothing usable and kicks off a background renewal, which reads the
	// record, finds the key that was just revoked, and spends it. The far side
	// answers "unrecognized or disabled peer key" and the operator watches
	// their correct paste produce an authentication failure.
	//
	// So the key lands first. A renewal racing this now exchanges the code that
	// actually exists, and the worst case is that it wins and the exchange
	// below finds the credential already there — which is the right answer
	// arriving by the other door.
	p.Key = strings.TrimSpace(key)
	RootDB.Set(remotePeersTable, name, p)
	InvalidatePeerResolution()
	clearPeerTokens(name)
	// Through SaveRemotePeer, not a field write: the new code has to be probed,
	// exchanged and have its capabilities re-read, because a re-issued key is
	// commonly a re-scoped one too.
	return SaveRemotePeer(ctx, name, p.BaseURL, key)
}

// RefreshRemotePeer re-probes a stored peer and records what came back,
// including a failure — a peer that has stopped answering should say so in the
// table rather than looking healthy until something tries to use it.
func RefreshRemotePeer(ctx context.Context, name string) (RemotePeer, error) {
	p, ok := GetRemotePeer(name)
	if !ok {
		return RemotePeer{}, fmt.Errorf("no peer named %q", name)
	}
	// Renew before probing rather than after a 401. The manifest is the first
	// call of every refresh cycle, so if the credential has aged out this is
	// where it shows, and recovering here keeps the failure off every
	// capability that would otherwise discover it independently.
	if p.UseTokens {
		if terr := EnsurePeerToken(ctx, p); terr != nil {
			Debug("[peer] %q credential renewal before refresh: %v", p.Name, terr)
		}
		p, _ = GetRemotePeer(p.Name)
	}
	m, err := ProbeRemotePeer(ctx, p.BaseURL, PeerCredential(p))
	p.LastChecked = time.Now().Format(time.RFC3339)
	if err != nil {
		p.LastError = err.Error()
		RootDB.Set(remotePeersTable, p.Name, p)
		InvalidatePeerResolution()
		return p, err
	}
	p.LastError = ""
	p.Caps = refreshedCaps(p.Caps, m)
	p.Instance = m.Instance
	p.UseTokens = adoptPeerTokenFlow(p, m)
	if m.Embeddings != nil {
		p.EmbedModel, p.EmbedDim = m.Embeddings.Model, m.Embeddings.Dim
	}
	p.TranscribeModel, p.SearchProvider = "", ""
	if m.Transcribe != nil {
		p.TranscribeModel = m.Transcribe.Model
	}
	if m.Search != nil {
		p.SearchProvider = m.Search.Provider
	}
	// Replaced wholesale, not merged: an appliance withdrawn on the far side
	// must disappear here at the next check rather than lingering in the picker
	// as something that 403s when asked.
	//
	// But only when the far side actually ANSWERED the question. An instance
	// that is not serving investigate right now sends no list, and an absent
	// list is not the same statement as an empty one — reading it as "nothing
	// is granted any more" emptied the picker on the strength of a question
	// that was never asked.
	if servesCap(m, PeerCapInvestigate) {
		p.Investigable = m.Investigable
	}
	// A grant revoked on the far side stops offering a renderer here at the
	// next check, rather than leaving a backend in the picker that 403s.
	syncPeerImages(&p, m.Images)
	RootDB.Set(remotePeersTable, p.Name, p)
	InvalidatePeerResolution()
	// A peer that has just been switched onto the token flow by its own
	// manifest has no credential yet. Obtained here rather than left to the
	// first background renewal, so the refresh the operator is watching either
	// reports success or says why.
	if p.UseTokens {
		if terr := EnsurePeerToken(ctx, p); terr != nil {
			Log("[peer] %q needs credential exchange and could not get one: %v", p.Name, terr)
		}
		p, _ = GetRemotePeer(p.Name)
	}
	return p, nil
}

// RefreshRemotePeers re-probes every stored peer, so what this instance
// believes about the far side is refreshed by the clock rather than only by an
// operator opening the admin page and clicking Refresh.
//
// Until this existed, a peer's record was written once and never revisited. A
// renderer reshaped on the far side, a capability withdrawn, or a local build
// that changed how a peer's backends are translated all left a stale connector
// in place indefinitely — and the failure surfaced as an error from the OTHER
// machine, about a backend the operator never configured by hand.
//
// Errors are logged, not returned: one unreachable peer must not stop the rest
// from being refreshed, and RefreshRemotePeer already records the failure on the
// peer record where the admin table shows it.
func RefreshRemotePeers(ctx context.Context) {
	for _, p := range ListRemotePeers() {
		if _, err := RefreshRemotePeer(ctx, p.Name); err != nil {
			Debug("[peer] refreshing %q: %v", p.Name, err)
		}
	}
}

// Runs at scheduler start and every 30 minutes thereafter.
func init() {
	RegisterReconciler("peer_refresh", func(ctx context.Context) error {
		RefreshRemotePeers(ctx)
		return nil
	})
}

// GetRemotePeer looks one up by name.
func GetRemotePeer(name string) (RemotePeer, bool) {
	if RootDB == nil {
		return RemotePeer{}, false
	}
	var p RemotePeer
	ok := RootDB.Get(remotePeersTable, strings.TrimSpace(strings.ToLower(name)), &p)
	return p, ok
}

// ListRemotePeers returns every registered peer, by name.
func ListRemotePeers() []RemotePeer {
	if RootDB == nil {
		return nil
	}
	var out []RemotePeer
	for _, name := range RootDB.Keys(remotePeersTable) {
		var p RemotePeer
		if RootDB.Get(remotePeersTable, name, &p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeleteRemotePeer forgets a peer.
//
// Nothing configured FROM it resolves at save time any more. Embeddings,
// transcription and search all keep the peer NAME in their stored config
// (EmbeddingConfig.Provider, TranscribeConfig.Provider, WebSearchConfig.Source)
// and overlay the live record on every read — see resolveEmbeddingPeer,
// resolveTranscribePeer and resolveSearchPeer. Deleting the peer leaves those
// configs pointed at its last known endpoint, still carrying the key that
// worked, and each logs once to say so rather than blanking itself.
//
// An LLM tier is stricter: it stores "peer:<name>" with no usable cache
// alongside it, resolves through this lookup on every build (see
// NewLLMFromConfig), and so breaks at the next reload with an error naming the
// missing peer.
//
// The shared property, and the one that matters: the peer record is the only
// place a peer credential lives. That is what lets a rotated key take effect
// with no edit anywhere, and what a rotating credential scheme would depend on.
func DeleteRemotePeer(name string) bool {
	if RootDB == nil {
		return false
	}
	name = strings.TrimSpace(strings.ToLower(name))
	p, ok := GetRemotePeer(name)
	if !ok {
		return false
	}
	// The backends this peer contributed go with it — leaving them would put
	// renderers in the picker pointed at a machine no longer configured, and
	// the credential holding its key would outlive the reason it existed.
	teardownPeerImages(p)
	RootDB.Unset(remotePeersTable, name)
	forgetPeerTokens(name)
	InvalidatePeerResolution()
	Log("[peer] removed remote %q", name)
	return true
}

// PeersOffering returns the peers that can serve a capability.
func PeersOffering(cap string) []RemotePeer {
	var out []RemotePeer
	for _, p := range ListRemotePeers() {
		if p.Offers(cap) {
			out = append(out, p)
		}
	}
	return out
}

// --- provider selection ------------------------------------------------------

// EmbeddingProviderLocal is the provider value meaning "this instance's own
// embedder", i.e. the endpoint/model typed into the form.
const EmbeddingProviderLocal = "local"

// peerProviderPrefix marks a provider value naming a registered peer.
const peerProviderPrefix = "peer:"

// PeerProviderValue is the select value for a peer.
func PeerProviderValue(name string) string { return peerProviderPrefix + name }

// PeerFromProvider returns the peer named by a provider value, if it names one.
func PeerFromProvider(provider string) (RemotePeer, bool) {
	provider = strings.TrimSpace(provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		return RemotePeer{}, false
	}
	return GetRemotePeer(strings.TrimPrefix(provider, peerProviderPrefix))
}

// ResolveEmbeddingProvider turns a submitted embedding config into the one to
// store, and validates the selection so the operator hears about a bad peer at
// SAVE time rather than at the next search.
//
// The fields it fills in are a last-known cache, not the operative values:
// GetEmbeddingConfig resolves the peer again on every read (see
// resolveEmbeddingPeer), so a rotated key or a moved peer takes effect without
// anyone editing this form. They are still written because a peer that is later
// deleted leaves nothing to resolve, and the last endpoint that worked is a
// better answer than a blank one.
//
// An unknown peer is an error rather than a silent fall back to local: falling
// back would point the vector store at a DIFFERENT embedder than the operator
// selected, and mixing spaces is the failure that never announces itself.
//
// EMPTY is not a choice. The form's own local option submits the string
// "local", so a blank provider means the field was never rendered — which
// happens whenever no peer currently OFFERS embeddings, since the dropdown is
// added and removed with the peers that populate it. Treating that as "local"
// converted a peer-backed config into a manual one on the next save: Provider
// reset while the resolved peer endpoint and its credential stayed behind, so
// the config kept pointing at the peer's URL with a credential nothing would
// ever refresh, and every embed answered 401 while the peer was healthy.
//
// It takes only a peer that briefly stops advertising embeddings and any save
// of that form while it is away. Nobody has to touch the provider, or know the
// dropdown was missing.
func ResolveEmbeddingProvider(cfg EmbeddingConfig) (EmbeddingConfig, error) {
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		if stored := GetEmbeddingConfig(); strings.HasPrefix(strings.TrimSpace(stored.Provider), peerProviderPrefix) {
			// A submission that says nothing about the provider cannot mean
			// "stop using the peer". Keep the selection and let the resolver
			// overlay it as usual.
			return cfg, nil
		}
	}
	if provider == "" || provider == EmbeddingProviderLocal {
		cfg.Provider = EmbeddingProviderLocal
		return cfg, nil
	}
	p, ok := PeerFromProvider(provider)
	if !ok {
		return cfg, fmt.Errorf("no peer named %q is registered — add it under Peers first",
			strings.TrimPrefix(provider, peerProviderPrefix))
	}
	if !p.Offers(PeerCapEmbeddings) {
		return cfg, fmt.Errorf("peer %q does not offer embeddings (it offers: %s)",
			p.Name, strings.Join(p.Caps, ", "))
	}
	cfg.Endpoint = p.EmbeddingsURL()
	cfg.Model = p.EmbedModel
	// PeerCredential, not the raw key. The key authenticates nothing since
	// exchange became mandatory (peer_token.go) — it is a pairing code. This
	// resolver is the CONFIG-time twin of the read-time overlay below, and it
	// snapshotting the key was a latent bug the whole time: it produced a
	// config that worked only while the static key was still accepted.
	cfg.APIKey = PeerCredential(p)
	return cfg, nil
}

// --- live peer resolution ----------------------------------------------------

// peerResolveTTL bounds how stale a resolved peer record may be.
//
// GetEmbeddingConfig sits on the embed path and EmbedVersion is consulted per
// cached vector, so hitting the store on every call would put a database read
// inside a comparison loop. A second is short enough that a key rotation takes
// effect while the operator is still looking at the screen, and long enough
// that a bulk ingest does not re-read the same record thousands of times.
const peerResolveTTL = time.Second

var (
	peerResolveMu    sync.Mutex
	peerResolveCache = map[string]peerResolveEntry{}
	// peerResolveWarned remembers which failures have already been logged, so a
	// peer deleted mid-ingest produces one line rather than one per chunk.
	peerResolveWarned = map[string]string{}
)

type peerResolveEntry struct {
	peer RemotePeer
	ok   bool
	at   time.Time
}

// lookupPeerCached is GetRemotePeer with a short TTL.
func lookupPeerCached(name string) (RemotePeer, bool) {
	key := strings.TrimSpace(strings.ToLower(name))
	peerResolveMu.Lock()
	if e, hit := peerResolveCache[key]; hit && time.Since(e.at) < peerResolveTTL {
		peerResolveMu.Unlock()
		return e.peer, e.ok
	}
	peerResolveMu.Unlock()

	p, ok := GetRemotePeer(key)
	peerResolveMu.Lock()
	peerResolveCache[key] = peerResolveEntry{peer: p, ok: ok, at: time.Now()}
	peerResolveMu.Unlock()
	return p, ok
}

// InvalidatePeerResolution drops the cached lookups. Called when a peer record
// changes so an operator who just pasted a new key does not wait out the TTL.
func InvalidatePeerResolution() {
	peerResolveMu.Lock()
	peerResolveCache = map[string]peerResolveEntry{}
	peerResolveWarned = map[string]string{}
	peerResolveMu.Unlock()
}

// warnPeerResolveOnce logs a resolution problem the first time it is seen, and
// again only if the problem CHANGES.
//
// An EMPTY msg is the all-clear: callers pass it after a success so the next
// occurrence of the same problem is reported again rather than deduped against
// a fault that has since been fixed. It records that and says nothing — it used
// to log it, which put a bare "[peer]" line with no message after it in the log
// every time a peer recovered. Reading one of those next to a real failure, the
// obvious conclusion is that something went wrong and could not say what.
// warnManualPeerEndpointOnce catches a config typed at a peer's URL by hand
// instead of by selecting the peer.
//
// It looks identical in the admin form and it can never work. A peer endpoint
// authenticates ONLY an access token, minted by exchange; a key pasted into an
// API-key box is a pairing code, which those endpoints refuse by design. And
// because the config does not NAME a peer, none of the machinery that would
// have fixed it engages: no resolver overlays the live endpoint, no transport
// swaps in a live credential, and refreshing the peer changes nothing, because
// as far as this config is concerned there is no peer.
//
// The failure is then indistinguishable from a genuinely stale peer
// credential — same 401, same words from the far side — which is what makes it
// worth a line of its own rather than a shrug.
func warnManualPeerEndpointOnce(kind, provider, endpoint string) {
	if !strings.Contains(endpoint, "/api/peer/v1/") {
		return
	}
	warnPeerResolveOnce("manual:"+kind, fmt.Sprintf(
		"%s is pointed at a PEER endpoint (%s) but its provider is %q rather than a peer. "+
			"That address only accepts a token obtained by exchange, so a key pasted into the API-key "+
			"box is refused however correct it looks, and re-checking the peer cannot help because this "+
			"config does not name one. Re-select the peer in the %s settings instead of entering the "+
			"address by hand.",
		kind, endpoint, provider, kind))
}

func warnPeerResolveOnce(name, msg string) {
	peerResolveMu.Lock()
	last, seen := peerResolveWarned[name]
	if seen && last == msg {
		peerResolveMu.Unlock()
		return
	}
	peerResolveWarned[name] = msg
	peerResolveMu.Unlock()
	if strings.TrimSpace(msg) == "" {
		return
	}
	Log("[peer] %s", msg)
}

// resolveEmbeddingPeer overlays the CURRENT peer record onto a stored embedding
// config whose Provider names a peer.
//
// This is the live half of peer embedding, and it exists because the save-time
// resolution alone was wrong: it copied the peer's endpoint, model and key into
// the stored config, so rotating the peer's key left a configuration that
// looked correct and returned 401 with nothing on either screen to say which of
// the two records had gone stale. Reading the peer here means the stored config
// is a POINTER and there is only one place the key lives.
//
// A peer that has gone missing or stopped offering embeddings keeps the stored
// fields — the last endpoint that worked — and logs once. The alternative,
// blanking the config, turns every search into a silent no-result, which is the
// failure mode this whole area is trying to avoid.
func resolveEmbeddingPeer(cfg EmbeddingConfig) EmbeddingConfig {
	provider := strings.TrimSpace(cfg.Provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		warnManualPeerEndpointOnce("embeddings", provider, cfg.Endpoint)
		return cfg
	}
	name := strings.TrimPrefix(provider, peerProviderPrefix)
	p, ok := lookupPeerCached(name)
	if !ok {
		warnPeerResolveOnce(name, fmt.Sprintf(
			"embeddings are configured against peer %q, which is no longer registered — "+
				"still using its last known endpoint %s", name, cfg.Endpoint))
		return cfg
	}
	if !p.Offers(PeerCapEmbeddings) {
		warnPeerResolveOnce(name, fmt.Sprintf(
			"peer %q no longer offers embeddings (it offers: %s) — "+
				"still using its last known endpoint %s", name, strings.Join(p.Caps, ", "), cfg.Endpoint))
		return cfg
	}
	warnPeerResolveOnce(name, "") // clears the warning once the peer is healthy again
	cfg.Endpoint = p.EmbeddingsURL()
	cfg.APIKey = PeerCredential(p)
	// The MODEL is only overridden when the peer reports one. A peer serving a
	// single-model backend advertises no model name, and overwriting a
	// deliberately-set informational value with "" would change EmbedVersion
	// and invalidate every cached vector for no reason.
	if strings.TrimSpace(p.EmbedModel) != "" {
		cfg.Model = p.EmbedModel
	}
	return cfg
}
