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

// handlePeerKeys serves the collection: GET lists, POST mints.
func (a *AdminApp) handlePeerKeys(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.Write(peerKeysJSON())
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
			Body: ui.FormPanel{
				Source:      "api/peer-keys",
				Method:      "POST",
				SubmitLabel: "Mint key",
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
		{
			Title: "How a peer connects",
			Subtitle: "What to enter on the OTHER instance. Embeddings need no extra software there — " +
				"the peer's ordinary embedding settings point at this instance.",
			Body: ui.Card{HTML: peerConnectHelpHTML},
		},
	}
}

// peerConnectHelpHTML documents the consumer side. It is static text rather
// than a live-built URL because the address a peer must use is the one it can
// REACH this instance at, which this instance cannot know — behind a proxy or
// a tunnel, the host in an admin request is frequently not it.
const peerConnectHelpHTML = `
<p style="margin:0 0 .75rem">On the peer instance, open <strong>Admin &rsaquo; Capabilities &rsaquo; Embeddings</strong> and set:</p>
<ul style="margin:0 0 .75rem 1.1rem;padding:0;line-height:1.7">
  <li><strong>Endpoint</strong> — <code>https://THIS-HOST/api/peer/v1</code></li>
  <li><strong>Model</strong> — the model name this instance reports (see below)</li>
  <li><strong>API key</strong> — the peer key from the table above</li>
</ul>
<p style="margin:0 0 .75rem">Replace <code>THIS-HOST</code> with an address the peer can actually reach this machine at.</p>
<p style="margin:0 0 .75rem">To check a key and see what it may use, the peer can call:</p>
<pre style="margin:0 0 .75rem;padding:.6rem .8rem;overflow-x:auto"><code>curl -H "X-Gohort-Peer-Key: &lt;key&gt;" https://THIS-HOST/api/peer/manifest</code></pre>
<p style="margin:0;opacity:.75">The manifest lists every capability, whether this build serves it, and whether
that key was granted it — so a peer that gets refused can tell "not built yet" from "not yours".</p>
`
