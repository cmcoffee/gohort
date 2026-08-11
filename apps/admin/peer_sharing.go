package admin

// Resource sharing — the admin surface for lending this instance's
// infrastructure to another gohort instance.
//
// A peer key is minted here, granted a capability allowlist, and pasted into
// the OTHER instance's config. It is not a user account and cannot become one
// (see core/peer_key.go); everything this page can do is issue, scope, revoke
// and delete grants.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// peerKeyRow is one row of the keys table. Separate from core's PeerKey so the
// table gets display-shaped fields (enabled rather than disabled, so the toggle
// reads the way an operator expects) without those leaking into the stored
// record.
type peerKeyRow struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Caps     []string `json:"caps"`
	Key      string   `json:"key"`
	Enabled  bool     `json:"enabled"`
	RatePerM int      `json:"rate_per_min"`
	Created  string   `json:"created"`
	LastSeen string   `json:"last_seen"`
	Calls    int64    `json:"calls"`
}

// peerKeysJSON renders the issued keys for the table.
func peerKeysJSON() []byte {
	keys := ListPeerKeys()
	rows := make([]peerKeyRow, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, peerKeyRow{
			ID: k.ID, Label: k.Label, Caps: k.Caps, Key: k.Key,
			Enabled: !k.Disabled, RatePerM: k.RatePerM,
			Created: k.Created, LastSeen: k.LastSeen, Calls: k.Calls,
		})
	}
	b, _ := json.Marshal(rows)
	return b
}

// handlePeerKeys serves the collection listing that backs the table.
//
// GET only, and separate from minting on purpose. A FormPanel GETs its Source
// to load current values and POSTs that same state back, so pointing the mint
// form here made it load the LIST — an array — and post the array as the new
// key. The save failed with a decode error naming a struct the operator never
// saw. A read endpoint whose shape is a list cannot also be a form's source.
func (a *AdminApp) handlePeerKeys(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed — mint at /api/peer-keys/mint", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(peerKeysJSON())
}

// handlePeerKeyMint backs the mint form. GET returns an empty record so the
// form loads blank; POST issues the key.
func (a *AdminApp) handlePeerKeyMint(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// A blank record, not the key list. The form posts back whatever it
		// loaded, so what it loads has to be the shape it saves.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"label":"","caps":[],"rate_per_min":0}`))
	case http.MethodPost:
		var req struct {
			Label    string          `json:"label"`
			Caps     json.RawMessage `json:"caps"`
			RatePerM int             `json:"rate_per_min"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		// A "checklist" field saves as a JSON array, but a form that has never
		// been touched can send a string or nothing at all. Accept every shape
		// rather than rejecting a mint over the encoding of an empty list.
		caps := decodeCapsField(req.Caps)
		pk, err := MintPeerKey(req.Label, caps, req.RatePerM)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":  true,
			"id":  pk.ID,
			"key": pk.Key,
			"message": "Key minted for " + pk.Label + ". Copy it from the table below — " +
				"it goes in the peer's own configuration, not here.",
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// decodeCapsField normalizes the capability list out of a checklist field,
// which may arrive as a JSON array, a JSON-encoded string holding an array, or
// a bare comma-separated string.
func decodeCapsField(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
		var out []string
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// handlePeerKeyItem serves one key: PUT toggles enabled, DELETE removes it.
func (a *AdminApp) handlePeerKeyItem(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/peer-keys/")
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[:i]
	}
	if id == "" {
		http.Error(w, "missing key id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch, http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
			http.Error(w, "expected {\"enabled\": bool}", http.StatusBadRequest)
			return
		}
		if !SetPeerKeyDisabled(id, !*req.Enabled) {
			http.Error(w, "no such key", http.StatusNotFound)
			return
		}
	case http.MethodDelete:
		if !DeletePeerKey(id) {
			http.Error(w, "no such key", http.StatusNotFound)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// --- consuming side: peers this instance borrows FROM -----------------------

// peerRow is one row of the peers table.
type peerRow struct {
	Name        string   `json:"name"`
	Instance    string   `json:"instance"`
	BaseURL     string   `json:"base_url"`
	Caps        []string `json:"caps"`
	EmbedModel  string   `json:"embed_model"`
	Status      string   `json:"status"`
	LastChecked string   `json:"last_checked"`
}

// peersJSON renders registered peers for the table.
func peersJSON() []byte {
	peers := ListRemotePeers()
	rows := make([]peerRow, 0, len(peers))
	for _, p := range peers {
		status := "ok"
		if p.LastError != "" {
			// The error itself, not a flag: "unreachable" sends the operator
			// looking, where "did not recognize that key" answers it.
			status = p.LastError
		}
		rows = append(rows, peerRow{
			Name: p.Name, Instance: p.Instance, BaseURL: p.BaseURL, Caps: p.Caps,
			EmbedModel: p.EmbedModel, Status: status, LastChecked: p.LastChecked,
		})
	}
	b, _ := json.Marshal(rows)
	return b
}

// handlePeers lists registered peers (GET) — the table's source.
func (a *AdminApp) handlePeers(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed — add a peer at /api/peers/add", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(peersJSON())
}

// handlePeerAdd backs the add-peer form. Same split as the mint form: a form's
// Source must return the shape it saves, never a list.
func (a *AdminApp) handlePeerAdd(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"","base_url":"","key":""}`))
	case http.MethodPost:
		var req struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
			Key     string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		p, err := SaveRemotePeer(r.Context(), req.Name, req.BaseURL, req.Key)
		if err != nil {
			// The probe's message is the useful one — it distinguishes an
			// unreachable host from a revoked key from a key that grants
			// nothing. Pass it through verbatim.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"message": "Connected to " + p.Instance + ". It offers: " + strings.Join(p.Caps, ", ") +
				". Pick it as a provider in the relevant capability's settings.",
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePeerItem serves one peer: POST re-probes it, DELETE forgets it.
func (a *AdminApp) handlePeerItem(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/peers/")
	action := ""
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name, action = name[:i], name[i+1:]
	}
	if name == "" {
		http.Error(w, "missing peer name", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodDelete:
		if !DeleteRemotePeer(name) {
			http.Error(w, "no such peer", http.StatusNotFound)
			return
		}
	case r.Method == http.MethodPost && action == "refresh":
		if _, err := RefreshRemotePeer(r.Context(), name); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// EmbeddingProviderOptions builds the provider dropdown for the Embeddings
// settings: this instance, plus every peer that actually offers embeddings.
//
// Exported because the Embeddings section lives in page.go with the rest of the
// capability settings; the peer knowledge stays here.
func EmbeddingProviderOptions() []ui.SelectOption {
	out := []ui.SelectOption{{
		Value: EmbeddingProviderLocal,
		Label: "This instance",
		Help:  "Embed locally, using the endpoint and model below.",
	}}
	for _, p := range PeersOffering(PeerCapEmbeddings) {
		label := "Peer: " + p.Name
		if p.Instance != "" {
			label += " (" + p.Instance + ")"
		}
		help := "Embed on " + p.BaseURL
		if p.EmbedModel != "" {
			help += " using " + p.EmbedModel
		}
		if p.LastError != "" {
			help += " — last check failed: " + p.LastError
		}
		out = append(out, ui.SelectOption{Value: PeerProviderValue(p.Name), Label: label, Help: help})
	}
	return out
}

// peerCapOptions builds the capability checklist, marking the ones this build
// cannot serve yet. Offering them is deliberate: a grant made today starts
// working the day the capability ships, and the operator can see the roadmap
// without reading the source.
func peerCapOptions() []ui.SelectOption {
	help := map[string]string{
		PeerCapEmbeddings: "Let the peer embed text using this instance's embedder. Available now.",
		PeerCapImages:     "Image generation and editing on this instance's GPU. Not implemented yet — granting it now has no effect until it ships.",
		PeerCapModels:     "Chat completions against this instance's configured models. Not implemented yet.",
		PeerCapTranscode:  "Media transcoding and frame sampling. Not implemented yet.",
	}
	label := map[string]string{
		PeerCapEmbeddings: "Embeddings",
		PeerCapImages:     "Image generation",
		PeerCapModels:     "Models (chat)",
		PeerCapTranscode:  "Transcoding",
	}
	out := make([]ui.SelectOption, 0, len(PeerCapabilities()))
	for _, c := range PeerCapabilities() {
		out = append(out, ui.SelectOption{Value: c, Label: label[c], Help: help[c]})
	}
	return out
}

// peerSharingSections builds the Resource Sharing surface: mint above, issued
// keys below.
func peerSharingSections() []ui.Section {
	return []ui.Section{
		{
			Title: "Resource Sharing",
			Subtitle: "Let another gohort instance use this one's infrastructure. Mint a key, grant it " +
				"only the capabilities you mean to lend, and paste it into the OTHER instance's " +
				"configuration. A peer key is not an account: it cannot sign in, read conversations, " +
				"or reach anything outside the capabilities checked here.",
			// The mint form and the instructions for using what it mints are one
			// task, so they are one pane. Split across two rail items, the half
			// that answers "now what do I do with this key" sat behind a
			// separate click from the half that produced it.
			Body: ui.Stack{Children: []ui.Component{
				ui.Card{HTML: peerConnectHelpHTML},
				ui.FormPanel{
					// Its OWN endpoint, not the list. A FormPanel loads its
					// Source and posts that state back, so a source returning
					// the key list would make the form save an array.
					Source:      "api/peer-keys/mint",
					Method:      "POST",
					SubmitLabel: "Mint key",
					// Refresh the table below once a key exists — the form and
					// the list no longer share an endpoint, so this no longer
					// happens on its own.
					Invalidate: []string{"api/peer-keys"},
					Fields: []ui.FormField{
						{Field: "label", Label: "Issued to", Type: "text", Required: true,
							Placeholder: "craig's MacBook",
							Help:        "How you'll recognize this peer later. Shown only to you."},
						{Field: "caps", Label: "Capabilities", Type: "checklist",
							Options: peerCapOptions(),
							Help:    "The key can do these and nothing else. Add more later by minting a new key."},
						{Field: "rate_per_min", Label: "Calls per minute", Type: "number", Min: 0, Max: 100000,
							Help: "Ceiling on how hard this peer may work this instance. Leave 0 for the default (600/min)."},
					},
				},
			}},
		},
		{
			Title: "Peers",
			Subtitle: "Other instances THIS one can borrow from. Add a peer with the key it issued you, " +
				"and its capabilities become selectable in the matching settings — a peer offering " +
				"embeddings appears in the Embeddings provider dropdown.",
			Body: ui.Stack{Children: []ui.Component{
				ui.FormPanel{
					Source:      "api/peers/add",
					Method:      "POST",
					SubmitLabel: "Connect",
					Invalidate:  []string{"api/peers"},
					Fields: []ui.FormField{
						{Field: "name", Label: "Name", Type: "text", Required: true,
							Placeholder: "gpu-box",
							Help:        "Short local nickname — lowercase letters, digits, - or _. How you'll pick it from a dropdown."},
						{Field: "base_url", Label: "Address", Type: "text", Required: true,
							Placeholder: "https://gpu-box.example",
							Help:        "Where this machine can reach that instance. Pasting a /api/peer/... path is fine — it gets trimmed."},
						{Field: "key", Label: "Peer key", Type: "text", Required: true,
							Help: "Minted on THAT instance under Capabilities › Resource Sharing. Connecting checks it immediately and reports what it grants."},
					},
				},
				ui.Table{
					Source: "api/peers",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Label: "Name", Flex: 1},
						{Field: "instance", Label: "Host", Flex: 1, Mute: true},
						{Field: "caps", Label: "Offers", Type: "pills", Flex: 2},
						{Field: "status", Label: "Status", Flex: 2, Mute: true},
						{Field: "last_checked", Label: "Checked", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Re-check", PostTo: "api/peers/{name}/refresh",
							Method: "POST", Compact: true},
						{Type: "button", Label: "Forget", PostTo: "api/peers/{name}", Method: "DELETE",
							Variant: "danger", Compact: true,
							Confirm: "Forget this peer? Anything already configured to use it keeps working — " +
								"its address and key were copied into that setting when you selected it — but it " +
								"stops appearing as a choice."},
					},
				},
			}},
		},
		{
			Title: "Shared With",
			Subtitle: "Keys issued to peer instances. The key column is the secret itself — it is shown so you " +
				"can paste it into the peer; treat it like a password. Turn a key off to cut a peer's access " +
				"immediately without losing the record of what was granted.",
			Body: ui.Table{
				Source: "api/peer-keys",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "label", Label: "Issued to", Flex: 2},
					{Field: "caps", Label: "Granted", Type: "pills", Flex: 2},
					{Field: "key", Label: "Key", Flex: 3, Mute: true},
					{Field: "last_seen", Label: "Last used", Format: "reltime", Mute: true},
					{Field: "calls", Label: "Calls", Format: "thousands", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "toggle", Field: "enabled", Label: "Enabled",
						PostTo: "api/peer-keys/{id}", Method: "PUT", Leading: true},
					{Type: "button", Label: "Delete", PostTo: "api/peer-keys/{id}", Method: "DELETE",
						Variant: "danger", Compact: true,
						Confirm: "Delete this peer key? The peer using it loses access immediately and the key cannot be recovered."},
				},
			},
		},
	}
}

// peerConnectHelpHTML documents the consumer side. It is static text rather
// than a live-built URL because the address a peer must use is the one it can
// REACH this instance at, which this instance cannot know — behind a proxy or
// a tunnel, the host in an admin request is frequently not it.
const peerConnectHelpHTML = `
<p style="margin:0 0 .75rem"><strong>To let another instance use this one:</strong> mint a key below, then on
that instance open <em>Admin &rsaquo; Capabilities &rsaquo; Resource Sharing &rsaquo; Peers</em>, and enter its
address plus the key. It checks the connection immediately and reports what the key grants.</p>
<p style="margin:0 0 .75rem">The address is whatever <em>that</em> machine can reach <em>this</em> one at —
this instance cannot work that out for you, since behind a proxy or a tunnel the host in your browser's URL
is usually not it.</p>
<p style="margin:0 0 .5rem">Once connected, the peer appears as a provider option in the matching setting —
an embeddings grant shows up in that instance's <em>Embeddings</em> provider dropdown. To check a key by hand:</p>
<pre style="margin:0 0 .75rem;padding:.6rem .8rem;overflow-x:auto"><code>curl -H "X-Gohort-Peer-Key: &lt;key&gt;" https://THIS-HOST/api/peer/manifest</code></pre>
<p style="margin:0;opacity:.75">The manifest names every capability, whether this build serves it, and whether
that key was granted it — so a refusal tells you which of the two it is.</p>
`
