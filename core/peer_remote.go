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
	"net/http"
	"regexp"
	"sort"
	"strings"
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
	Name        string   `json:"name"`     // local nickname
	BaseURL     string   `json:"base_url"` // https://host — no /api/peer suffix
	Key         string   `json:"key"`      // the peer key that instance issued
	Caps        []string `json:"caps"`     // served AND granted, from the manifest
	Instance    string   `json:"instance,omitempty"`
	EmbedModel  string   `json:"embed_model,omitempty"`
	EmbedDim    int      `json:"embed_dim,omitempty"`
	LastChecked string   `json:"last_checked,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	// ImageConnectors names the local rest_image connectors provisioned from
	// this peer's advertised renderers. Stored rather than recomputed: teardown
	// reads this list, so a connector the operator made themselves can never be
	// swept up by a peer being forgotten.
	ImageConnectors []string `json:"image_connectors,omitempty"`
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
	// Renderers become local backends immediately. A peer that announces it
	// offers rendering and then does not appear in the picker is indisputably
	// broken from the operator's side.
	syncPeerImages(&p, m.Images)
	RootDB.Set(remotePeersTable, name, p)
	Log("[peer] added remote %q at %s offering %s", name, p.BaseURL, strings.Join(caps, ", "))
	return p, nil
}

// RefreshRemotePeer re-probes a stored peer and records what came back,
// including a failure — a peer that has stopped answering should say so in the
// table rather than looking healthy until something tries to use it.
func RefreshRemotePeer(ctx context.Context, name string) (RemotePeer, error) {
	p, ok := GetRemotePeer(name)
	if !ok {
		return RemotePeer{}, fmt.Errorf("no peer named %q", name)
	}
	m, err := ProbeRemotePeer(ctx, p.BaseURL, p.Key)
	p.LastChecked = time.Now().Format(time.RFC3339)
	if err != nil {
		p.LastError = err.Error()
		RootDB.Set(remotePeersTable, p.Name, p)
		return p, err
	}
	p.LastError = ""
	p.Caps = usableCaps(m)
	p.Instance = m.Instance
	if m.Embeddings != nil {
		p.EmbedModel, p.EmbedDim = m.Embeddings.Model, m.Embeddings.Dim
	}
	// A grant revoked on the far side stops offering a renderer here at the
	// next check, rather than leaving a backend in the picker that 403s.
	syncPeerImages(&p, m.Images)
	RootDB.Set(remotePeersTable, p.Name, p)
	return p, nil
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

// DeleteRemotePeer forgets a peer. Anything already configured FROM it keeps
// working: the endpoint and key were resolved into the consuming config at save
// time, so removing the peer record does not silently disable embeddings
// mid-conversation. It only stops appearing as a choice.
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
// store. When Provider names a peer, the endpoint, model and key come from that
// peer's record rather than from whatever the (hidden) fields held — so the
// stored config is a complete, ordinary one that Embed can use with no peer
// awareness at all.
//
// An unknown peer is an error rather than a silent fall back to local: falling
// back would point the vector store at a DIFFERENT embedder than the operator
// selected, and mixing spaces is the failure that never announces itself.
func ResolveEmbeddingProvider(cfg EmbeddingConfig) (EmbeddingConfig, error) {
	provider := strings.TrimSpace(cfg.Provider)
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
	cfg.APIKey = p.Key
	return cfg, nil
}
