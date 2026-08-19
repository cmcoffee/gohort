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
	"fmt"
	"net/http"
	"sort"
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
	Enabled  bool     `json:"enabled"`
	RatePerM int      `json:"rate_per_min"`
	Created  string   `json:"created"`
	LastSeen string   `json:"last_seen"`
	Calls    int64    `json:"calls"`
	// There is deliberately NO Key field. The secret is shown once, when it is
	// minted or re-issued, and never again — so it must not travel in the list
	// payload either. A column an operator cannot read is still a secret sitting
	// in a JSON response, in a browser cache, and in whatever proxy log is
	// between the two.
	//
	// Paired takes its place: the code's STATE is what the table can honestly
	// report. Empty means nobody has connected with it yet; a timestamp means it
	// was spent and this peer is running on rotating tokens.
	// No omitempty: the table binds a column to this field, and a key the row
	// JSON omits reads to the renderer as a column bound to nothing.
	Paired string `json:"paired"`
	// Status renders that same fact as a sentence, because "empty" is not a
	// state anybody reads correctly in a column of timestamps.
	Status string `json:"status"`
}

// peerKeysJSON renders the issued keys for the table.
func peerKeysJSON() []byte {
	keys := ListPeerKeys()
	rows := make([]peerKeyRow, 0, len(keys))
	for _, k := range keys {
		status := "Not connected yet — the code is still unspent"
		if strings.TrimSpace(k.Paired) != "" {
			status = "Paired — running on rotating tokens"
		}
		if k.Disabled {
			status = "Disabled"
		}
		rows = append(rows, peerKeyRow{
			ID: k.ID, Label: k.Label, Caps: peerCapShortLabels(k.Caps),
			Enabled: !k.Disabled, RatePerM: k.RatePerM,
			Created: k.Created, LastSeen: k.LastSeen, Calls: k.Calls,
			Paired: k.Paired, Status: status,
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
			"ok":      true,
			"id":      pk.ID,
			"key":     pk.Key,
			"label":   pk.Label,
			"message": "Key minted for " + pk.Label + ". This is the only time it is shown.",
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

// handlePeerKeyItem serves one key: GET returns it (for the Grants modal to
// prefill), PUT toggles enabled OR re-grants capabilities, DELETE removes it.
func (a *AdminApp) handlePeerKeyItem(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/peer-keys/")
	// A trailing segment names an ACTION on the key rather than a field of it.
	// Re-pairing is not an edit to a value the table shows — it replaces the
	// secret and revokes what was issued against it — so it gets a verb of its
	// own instead of another optional field on the PUT body.
	action := ""
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id, action = id[:i], strings.Trim(id[i+1:], "/")
	}
	if id == "" {
		http.Error(w, "missing key id", http.StatusBadRequest)
		return
	}
	if action != "" {
		if action != "repair" {
			http.Error(w, "unknown action "+action, http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "POST to re-pair", http.StatusMethodNotAllowed)
			return
		}
		pk, rerr := RepairPeerKey(id)
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusBadRequest)
			return
		}
		Log("[admin] user %q re-paired peer key %q", AuthCurrentUser(r), pk.Label)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "key": pk.Key})
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Per-record GET, for the Grants modal to prefill from. Returns the
		// same shape the PUT accepts.
		for _, k := range ListPeerKeys() {
			if k.ID == id {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"id": k.ID, "label": k.Label, "caps": k.Caps,
					"appliances": peerKeyScopeValues(k),
				})
				return
			}
		}
		http.Error(w, "no such key", http.StatusNotFound)
		return
	case http.MethodPut, http.MethodPatch, http.MethodPost:
		// Two different edits share this verb: the row toggle sends
		// {"enabled":bool}, the Grants modal sends {"caps":[...]}. Dispatch on
		// which one is present rather than on the method, so neither has to
		// know about the other.
		var req struct {
			Enabled    *bool     `json:"enabled"`
			Caps       *[]string `json:"caps"`
			Owner      *string   `json:"owner"`
			Appliances *[]string `json:"appliances"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			(req.Enabled == nil && req.Caps == nil && req.Owner == nil && req.Appliances == nil) {
			http.Error(w, "expected {\"enabled\": bool}, {\"caps\": [...]}, or {\"owner\", \"appliances\"}", http.StatusBadRequest)
			return
		}
		if req.Caps != nil {
			pk, cerr := SetPeerKeyCaps(id, *req.Caps)
			if cerr != nil {
				http.Error(w, cerr.Error(), http.StatusBadRequest)
				return
			}
			Log("[admin] user %q re-granted peer key %q: %s",
				AuthCurrentUser(r), pk.Label, strings.Join(pk.Caps, ", "))
		}
		// Scope — whose appliances, and which. Sent together by the Grants
		// modal: setting one without the other would leave a key naming
		// machines with nobody to run as, or an owner reaching nothing.
		if req.Owner != nil || req.Appliances != nil {
			owner, apps := "", []string{}
			if req.Owner != nil {
				owner = *req.Owner
			}
			if req.Appliances != nil {
				// The checklist carries "<user>:<id>" values, so the owner
				// comes out of the selection rather than being asked for
				// separately — see peerApplianceScopeOptions.
				derived, ids, derr := splitPeerApplianceScope(*req.Appliances)
				if derr != nil {
					http.Error(w, derr.Error(), http.StatusBadRequest)
					return
				}
				apps = ids
				if derived != "" {
					owner = derived
				}
			}
			pk, serr := SetPeerKeyScope(id, owner, apps)
			if serr != nil {
				http.Error(w, serr.Error(), http.StatusBadRequest)
				return
			}
			Log("[admin] user %q scoped peer key %q to owner=%q over %d appliance(s)",
				AuthCurrentUser(r), pk.Label, pk.Owner, len(pk.Appliances))
		}
		if req.Enabled != nil && !SetPeerKeyDisabled(id, !*req.Enabled) {
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
	Backends    int      `json:"backends"`
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
			Name: p.Name, Instance: p.Instance, BaseURL: p.BaseURL, Caps: peerCapShortLabels(p.Caps),
			EmbedModel: p.EmbedModel, Backends: len(p.ImageConnectors),
			Status: status, LastChecked: p.LastChecked,
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
	case r.Method == http.MethodGet && action == "key":
		// The blank record the update form loads. A FormPanel posts back what
		// it loaded, so what it loads has to be the shape it saves — and it
		// must never be the CURRENT key, which is a secret this side holds and
		// has no reason to redisplay.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":""}`))
		return
	case r.Method == http.MethodPost && action == "key":
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		p, err := UpdateRemotePeerKey(r.Context(), name, body.Key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		Log("[admin] user %q re-paired peer %q with a new key", AuthCurrentUser(r), name)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "Re-paired with " + p.Instance + ". It offers: " + strings.Join(p.Caps, ", ") + ".",
		})
		return
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

// TranscribeProviderOptions builds the provider dropdown for the Transcription
// settings: this instance, plus every peer that actually offers STT.
//
// Same shape and same reasons as EmbeddingProviderOptions — the peer knowledge
// stays in this file, the section that uses it lives in page.go.
func TranscribeProviderOptions() []ui.SelectOption {
	out := []ui.SelectOption{{
		Value: EmbeddingProviderLocal,
		Label: "This instance",
		Help:  "Transcribe locally, using the endpoint and model below.",
	}}
	for _, p := range PeersOffering(PeerCapTranscribe) {
		label := "Peer: " + p.Name
		if p.Instance != "" {
			label += " (" + p.Instance + ")"
		}
		help := "Transcribe on " + p.BaseURL
		if p.TranscribeModel != "" {
			help += " using " + p.TranscribeModel
		}
		if p.LastError != "" {
			help += " — last check failed: " + p.LastError
		}
		out = append(out, ui.SelectOption{Value: PeerProviderValue(p.Name), Label: label, Help: help})
	}
	return out
}

// SearchProviderOptions builds the "Search on" dropdown: this instance, plus
// every peer that actually offers search.
func SearchProviderOptions() []ui.SelectOption {
	out := []ui.SelectOption{{
		Value: EmbeddingProviderLocal,
		Label: "This instance",
		Help:  "Search with the provider configured below.",
	}}
	for _, p := range PeersOffering(PeerCapSearch) {
		label := "Peer: " + p.Name
		if p.Instance != "" {
			label += " (" + p.Instance + ")"
		}
		help := "Search through " + p.BaseURL
		if p.SearchProvider != "" {
			help += ", which uses " + p.SearchProvider
		}
		if p.LastError != "" {
			help += " — last check failed: " + p.LastError
		}
		out = append(out, ui.SelectOption{Value: PeerProviderValue(p.Name), Label: label, Help: help})
	}
	return out
}

// BrowseProviderOptions builds the "Render pages on" dropdown: this instance,
// plus every peer that actually offers browsing.
func BrowseProviderOptions() []ui.SelectOption {
	out := []ui.SelectOption{{
		Value: EmbeddingProviderLocal,
		Label: "This instance",
		Help:  "Render pages in this machine's own headless browser.",
	}}
	for _, p := range PeersOffering(PeerCapBrowse) {
		label := "Peer: " + p.Name
		if p.Instance != "" {
			label += " (" + p.Instance + ")"
		}
		help := "Render pages on " + p.BaseURL + " — this machine then needs no Chromium of its own"
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
// peerCapShortLabel is the human name for a capability, in the register a PILL
// needs: two or three words, no qualifiers.
//
// It exists because the two ends of a share were speaking different languages.
// The lending end offered "Run commands (the peer gets a shell on these
// systems…)" in its mint checklist, while the borrowing end's Offers column —
// and this end's own Granted column — printed the raw storage slug, so an
// operator reading the peer they had just connected saw "exec" and had to
// remember which of the friendly sentences that was. A grant nobody can name
// is a grant nobody audits.
//
// An UNKNOWN slug comes back unchanged rather than blank. Offers is filled from
// the far side's manifest, so a peer running a newer build can legitimately
// name a capability this one has never heard of, and printing it raw says
// exactly that — where a lookup miss rendered as an empty pill would read as a
// grant of nothing.
func peerCapShortLabel(cap string) string {
	switch cap {
	case PeerCapEmbeddings:
		return "Embeddings"
	case PeerCapImages:
		return "Image generation"
	case PeerCapTranscribe:
		return "Transcription"
	case PeerCapSearch:
		return "Web search"
	case PeerCapBrowse:
		return "Page rendering"
	case PeerCapModels:
		return "Inference"
	case PeerCapInvestigate:
		return "Investigate systems"
	case PeerCapKnowledge:
		return "Gathered knowledge"
	case PeerCapExec:
		return "Run commands"
	}
	return cap
}

// peerCapShortLabels maps a whole list, for the pill columns.
func peerCapShortLabels(caps []string) []string {
	if len(caps) == 0 {
		return nil
	}
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, peerCapShortLabel(c))
	}
	return out
}

func peerCapOptions() []ui.SelectOption {
	help := map[string]string{
		PeerCapEmbeddings: "Let the peer embed text using this instance's embedder.",
		PeerCapImages:     "Generate and edit images on this instance's GPU, including multi-image edits.",
		PeerCapTranscribe: "Turn speech into text using this instance's STT model — audio files, voice notes, the audio track of a video.",
		PeerCapSearch:     "Run web searches through this instance's search provider. Note this SPENDS a metered API key if one is configured here; it is rate-limited separately and more tightly than everything else.",
		PeerCapBrowse:     "Render pages in this instance's headless browser. Public web only — a peer can never reach this machine's private network through it.",
		PeerCapModels: "Let the peer run its LLM turns on THIS instance's model — the machine with the GPU does the thinking for the one without. " +
			"The peer configures an ordinary llama.cpp provider pointed here and everything works as if the model were local: streaming, tool calls, images, thinking budgets. " +
			"Only LOCAL backends are lent (llama.cpp and ollama). A hosted provider is never relayed, because that would spend this operator's API key on someone else's prompts.",
		PeerCapInvestigate: "Let the peer ask questions about systems THIS instance can reach — for a machine on a network the peer has no route to. " +
			"It sends a question; this instance runs the investigation itself, read-only, under its own approval rules, and returns prose. " +
			"No command from the peer is ever executed, and no credential leaves here. " +
			"Requires picking which systems below — the grant alone reaches nothing.",
		PeerCapKnowledge: "Let the peer hold a COPY of what this instance has already learned about those systems — " +
			"the structured docs and recorded facts — so it can answer from them instantly instead of asking every time. " +
			"Each item keeps the age it has here, so a copy of a two-month-old map reads as two months old over there. " +
			"Separate from Investigate on purpose: this one moves knowledge off this machine, the other only answers questions. " +
			"Uses the same system list below.",
		PeerCapExec: "Let the peer run commands on those systems through THIS instance's connection to them — the peer as a wire to a machine it cannot route to. " +
			"Understand what this is: a key granted it has a shell on the named systems, and the CALLING instance decides what runs, not this one. " +
			"There is no risk gate on this side, by design — that is what makes a peer-reached system behave exactly like a local one. " +
			"Grant it only to instances you own, and only for the systems below.",
	}
	// The checklist is a permission screen, so it qualifies each name — but it
	// BUILDS on the short label rather than restating it, so a capability reads
	// as the same thing here, in the Granted column, and on the far side's
	// Offers column. Restating them is how "exec" came to have three names.
	qualifier := map[string]string{
		PeerCapTranscribe:  " (speech-to-text)",
		PeerCapBrowse:      " (browser)",
		PeerCapModels:      " (the peer's turns execute on this machine's model)",
		PeerCapInvestigate: " (answers questions, runs no remote commands)",
		PeerCapKnowledge:   " (copies docs and facts to the peer)",
		PeerCapExec:        " (the peer gets a shell on these systems, gated on ITS side)",
	}
	label := map[string]string{}
	for _, c := range PeerCapabilities() {
		label[c] = peerCapShortLabel(c) + qualifier[c]
	}
	out := make([]ui.SelectOption, 0, len(PeerCapabilities()))
	for _, c := range PeerCapabilities() {
		h := help[c]
		// Derived, not hand-written: a capability that ships stops being
		// labelled unbuilt without anyone remembering to edit this copy, and one
		// that has not shipped can never be advertised as working.
		switch {
		case PeerCapabilityServed(c):
			h += " Available now."
		case c == PeerCapModels:
			// Not "unbuilt" — built, and idle because of how THIS instance is
			// configured. Told apart because the operator can fix one of these
			// and not the other.
			h += " Nothing to lend right now: this instance's models are on a hosted provider, " +
				"and only a local llama.cpp or ollama is lent. Granting it is harmless and starts " +
				"working if a local model is configured here."
		default:
			h += " Not implemented yet — granting it now has no effect until it ships."
		}
		out = append(out, ui.SelectOption{Value: c, Label: label[c], Help: h})
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
					// Shown once, here, and never again — the table cannot
					// display it and the list payload no longer carries it.
					OnSuccess: "peer_key_issued",
					// Refresh the table below once a key exists — the form and
					// the list no longer share an endpoint, so this no longer
					// happens on its own.
					Invalidate: []string{"api/peer-keys"},
					Fields: []ui.FormField{
						{Field: "label", Label: "Issued to", Type: "text", Required: true,
							// The OTHER end of the pair the Peers form below names.
							// This panel lends, that one borrows, so the two examples
							// have to be opposite roles: a key is issued to the
							// instance doing the borrowing (app-box), and the peer you
							// add is the one that has what you want (gpu-box). Naming
							// both ends gpu-box would teach the direction backwards.
							//
							// It read "craig's MacBook", which pointed at the wrong
							// key entirely: a laptop running the desktop app
							// authenticates with a DESKTOP key (core/desktop_key.go,
							// auto-provisioned, its own endpoint), never with one of
							// these. Somebody could mint a peer key trying to connect
							// a laptop and find nothing that accepts it.
							Placeholder: "app-box",
							Help:        "Which gohort instance you are lending to — how you'll recognize it later. Shown only to you."},
						{Field: "caps", Label: "Capabilities", Type: "checklist",
							Options: peerCapOptions(),
							Help:    "The key can do these and nothing else. Change them later with the Grants button on the key row \u2014 no need to mint a new one."},
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
				"embeddings appears in the Embeddings provider dropdown, and one offering rendering " +
				"contributes its image backends to the picker, edits included.",
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
						// How many local image backends this peer contributed. A
						// grant with no renderers behind it looks identical to a
						// working one until you open the picker and find nothing.
						{Field: "backends", Label: "Renderers", Mute: true},
						{Field: "status", Label: "Status", Flex: 2, Mute: true},
						{Field: "last_checked", Label: "Checked", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Re-check", PostTo: "api/peers/{name}/refresh",
							Method: "POST", Compact: true},
						// The door out of a failed Re-check. A peer key is spent
						// at pairing and dies when the far side re-issues it, so
						// "this peer stopped working" is most often answered by a
						// new code — and until this existed the only way to enter
						// one was to Forget the peer and add it again, which drops
						// the image backends it contributed on the way past.
						ui.ModalAction("Update key", ui.FormPanel{
							Source:      "api/peers/{name}/key",
							PostURL:     "api/peers/{name}/key",
							Method:      "POST",
							SubmitLabel: "Re-pair",
							Invalidate:  []string{"api/peers", "api/image-backends"},
							Fields: []ui.FormField{{
								Field: "key", Label: "New peer key", Type: "text", Required: true,
								Help: "Issued on the OTHER instance — Capabilities › Resource Sharing › Re-issue key. " +
									"Its address and name here are kept; the key is exchanged for fresh credentials immediately, " +
									"and what it may reach is re-read at the same time.",
							}},
						}),
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
					// No Key column. The secret is shown once when it is issued
					// and is not in this payload at all — a column an operator
					// cannot read would still be a secret in a browser cache and
					// in whatever proxy log sits between here and there.
					{Field: "status", Label: "State", Flex: 2, Mute: true},
					{Field: "paired", Label: "Paired", Format: "reltime", Mute: true},
					{Field: "last_seen", Label: "Last used", Format: "reltime", Mute: true},
					{Field: "calls", Label: "Calls", Format: "thousands", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "toggle", Field: "enabled", Label: "Enabled",
						PostTo: "api/peer-keys/{id}", Method: "PUT", Leading: true},
					// Re-granting an existing key, rather than delete-and-mint.
					// The secret survives, so a peer already holding it picks up
					// a newly-shipped capability without anything being
					// re-pasted or re-probed on the other machine.
					ui.ModalAction("Grants", ui.FormPanel{
						Source:      "api/peer-keys/{id}",
						PostURL:     "api/peer-keys/{id}",
						Method:      "PUT",
						SubmitLabel: "Save grants",
						Invalidate:  []string{"api/peer-keys"},
						Fields: []ui.FormField{
							{Field: "caps", Label: "Capabilities", Type: "checklist",
								Options: peerCapOptions(),
								Help:    "The key can do these and nothing else. Removing one takes effect immediately; the peer keeps the same key either way."},
							{Field: "appliances", Label: "Systems this key may reach", Type: "checklist",
								Options: peerApplianceScopeOptions(),
								Help: "For the Investigate, Share-knowledge and Run-commands grants, and only these — there is no \"all systems\". " +
									"The peer sends a QUESTION; this instance runs the investigation itself, on its own network, read-only. " +
									"Credentials never leave here. Pick systems belonging to ONE user: the investigation runs as them, so it reaches exactly what they can. " +
									"Leave empty and an Investigate grant reaches nothing."},
						},
					}),
					// The replacement for re-displaying a secret that is no longer
					// displayable. A code is spent the moment its peer pairs, so
					// there is never anything current to show — and what an
					// operator staring at a disconnected peer actually wants is
					// not the old string, it is this peer connected again.
					//
					// A client action rather than a plain POST because the reply
					// carries the new secret, and it has to reach a surface
					// somebody can copy from before it is gone for good.
					{Type: "button", Label: "Re-issue key", PostTo: "peer_key_reissue", Method: "client",
						Compact: true},
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

// peerApplianceScopeOptions lists every user's investigable systems as
// "<user>:<appliance-id>".
//
// The owner is folded INTO the value rather than sitting in its own field
// because a FormPanel field cannot fetch options that depend on another field's
// value — and the encoding turns out to be the better design anyway. A key has
// exactly one owner, and with the owner carried on each option the rule is
// enforced by what the operator can pick rather than by a validation message
// after the fact.
func peerApplianceScopeOptions() []ui.SelectOption {
	// AuthDB is a FUNC VAR — calling it unguarded panics rather than returning
	// nil, which is how this reaches the page builder before auth is wired (and
	// in every test binary). Third time today; the guard is cheap.
	if AuthDB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ui.SelectOption
	for _, u := range AuthListUsers(authDB) {
		user := strings.TrimSpace(u.Username)
		if user == "" {
			continue
		}
		for _, g := range ReferenceGroups(user) {
			if g.Kind != "system" {
				continue
			}
			for _, it := range g.Items {
				label := user + " · " + it.Name
				if strings.TrimSpace(it.Desc) != "" {
					label += " (" + it.Desc + ")"
				}
				out = append(out, ui.SelectOption{Value: user + ":" + it.ID, Label: label})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// splitPeerApplianceScope turns the picked "<user>:<id>" values back into an
// owner and an id list, refusing a selection that spans two users.
//
// Refusing rather than picking one: a key that reached two people's machines
// would be a permission nobody could describe in a sentence, and silently
// dropping half the selection is worse than saying no.
func splitPeerApplianceScope(picked []string) (owner string, ids []string, err error) {
	for _, p := range picked {
		u, id, ok := strings.Cut(strings.TrimSpace(p), ":")
		u, id = strings.TrimSpace(u), strings.TrimSpace(id)
		if !ok || u == "" || id == "" {
			continue
		}
		if owner == "" {
			owner = u
		} else if owner != u {
			return "", nil, fmt.Errorf(
				"a key can reach one user's systems, not several — %q and %q were both selected", owner, u)
		}
		ids = append(ids, id)
	}
	return owner, ids, nil
}

// peerKeyScopeValues renders a key's stored scope back into the option values
// the checklist uses, so the modal prefills with what is actually granted.
func peerKeyScopeValues(k PeerKey) []string {
	if strings.TrimSpace(k.Owner) == "" {
		return nil
	}
	out := make([]string, 0, len(k.Appliances))
	for _, id := range k.Appliances {
		out = append(out, k.Owner+":"+id)
	}
	return out
}

// LLMProviderOptions builds the provider dropdown for a model tier: the local
// and hosted providers, plus every peer that will actually run inference.
//
// Shared by both tiers so a peer appears in each. usePrimary adds the Lead
// tier's "(use primary)" entry, which the Worker tier has no meaning for.
func LLMProviderOptions(usePrimary bool) []ui.SelectOption {
	var out []ui.SelectOption
	if usePrimary {
		out = append(out, ui.SelectOption{Value: "", Label: "(use primary)",
			Help: "Routes lead stages to the worker model."})
	}
	for _, o := range []ui.SelectOption{
		{Value: "ollama", Label: "Ollama"},
		{Value: "llama.cpp", Label: "llama.cpp"},
		{Value: "anthropic", Label: "Anthropic"},
		{Value: "openai", Label: "OpenAI"},
		{Value: "gemini", Label: "Gemini"},
		{Value: "bedrock", Label: "AWS Bedrock"},
	} {
		out = append(out, o)
	}
	for _, p := range PeersOffering(PeerCapModels) {
		label := "Peer: " + p.Name
		if p.Instance != "" {
			label += " (" + p.Instance + ")"
		}
		help := "Run this tier's turns on " + p.BaseURL +
			" — this machine sends the prompt and the peer's GPU does the work. " +
			"Leave the endpoint, model and key below blank: they are read from the peer record every time the model is built, " +
			"so rotating the peer's key takes effect without editing anything here."
		if p.LastError != "" {
			help += " Last check failed: " + p.LastError
		}
		out = append(out, ui.SelectOption{Value: PeerProviderValue(p.Name), Label: label, Help: help})
	}
	return out
}

// --- show-once secret --------------------------------------------------------

// peerKeyShowOnceJS renders the one moment a peer key is visible.
//
// The key is no longer a standing credential: it is a pairing code, spent the
// first time its peer exchanges it, and after that there is nothing current to
// display. So the table does not carry it and this modal is the only surface it
// ever appears on — which makes the modal's job narrow and specific. It has to
// be copyable in one gesture, it has to say plainly that this is the last time,
// and it must not be dismissable by accident before either has happened.
//
// Deliberately NOT a toast. A toast holds one line for three seconds and takes
// it away, which for a secret somebody has to paste into another machine's
// configuration is the difference between a working link and re-issuing.
const peerKeyShowOnceJS = `
window.__peerKeyShowOnce = function(label, key) {
  window.uiOpenModal({
    title: 'Peer key for ' + (label || 'this peer'),
    subtitle: 'Copy it now — this is the only time it is shown. It goes in the OTHER instance\'s Peers form, ' +
      'where it is exchanged once for rotating credentials. If it is lost, re-issue the key from this table.',
    width: '620px',
    actions: [{label: 'Done', primary: true}],
    mount: function(box) {
      var wrap = document.createElement('div');
      wrap.style.cssText = 'display:flex;flex-direction:column;gap:0.6rem';
      var code = document.createElement('code');
      code.textContent = key;
      code.style.cssText = 'font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:0.82rem;' +
        'word-break:break-all;padding:0.6rem 0.7rem;border:1px solid var(--accent);border-radius:8px;background:var(--bg-2)';
      var copy = document.createElement('button');
      copy.className = 'ui-row-btn';
      copy.textContent = 'Copy';
      copy.style.alignSelf = 'flex-start';
      copy.addEventListener('click', function() {
        if (navigator.clipboard) navigator.clipboard.writeText(key);
        copy.textContent = 'Copied';
        setTimeout(function(){ copy.textContent = 'Copy'; }, 1400);
      });
      wrap.appendChild(code);
      wrap.appendChild(copy);
      box.appendChild(wrap);
    }
  });
};
`

// peerKeyIssuedAction is the mint form's OnSuccess: the server just made a
// secret the form could not have known, and this is its one showing.
const peerKeyIssuedAction = `function(ctx){
  var r = (ctx && ctx.response) || {};
  if (r.key) window.__peerKeyShowOnce(r.label, r.key);
}`

// peerKeyReissueAction re-issues a key from its row: confirm, POST, show the
// new secret once.
//
// A client action rather than a plain POST row-button because the reply CARRIES
// that secret. A row button would swallow it into a reload, and the operator
// would be left with a peer that can no longer connect and no way to reconnect
// it short of re-issuing again.
const peerKeyReissueAction = `async function(ctx){
  var rec = (ctx && ctx.record) || {};
  var ok = await (window.uiConfirm || window.confirm)(
    'Re-issue the key for ' + (rec.label || 'this peer') + '? Its current credentials stop working immediately, ' +
    'and it cannot reconnect until the new key is pasted into its Peers form. Capabilities and scope are kept.');
  if (!ok) return;
  if (ctx.button) { ctx.button.disabled = true; }
  try {
    var resp = await fetch('api/peer-keys/' + encodeURIComponent(rec.id) + '/repair',
      {method: 'POST', credentials: 'same-origin'});
    if (!resp.ok) throw new Error(await resp.text() || ('HTTP ' + resp.status));
    var d = await resp.json();
    if (ctx.reload) ctx.reload();
    window.__peerKeyShowOnce(rec.label, d.key);
  } catch (e) {
    (window.uiAlert || window.alert)('Could not re-issue that key: ' + (e && e.message || e));
  } finally {
    if (ctx.button) { ctx.button.disabled = false; }
  }
}`
